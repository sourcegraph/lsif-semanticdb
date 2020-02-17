package index

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/gogo/protobuf/proto"
	"github.com/sourcegraph/lsif-go/protocol"
	"github.com/sourcegraph/lsif-semanticdb/internal/log"
	pb "github.com/sourcegraph/lsif-semanticdb/internal/proto"
)

const LanguageScala = "scala"

// Indexer reads SemanticDB files and outputs LSIF data.
type Indexer interface {
	Index() (*Stats, error)
}

// Stats contains statistics of data processed during index.
type Stats struct {
	NumPkgs     int
	NumFiles    int
	NumDefs     int
	NumElements int
}

// indexer keeps track of all information needed to generate an LSIF dump.
type indexer struct {
	projectRoot string
	toolInfo    protocol.ToolInfo
	w           *protocol.Writer

	// Type correlation
	files map[string]*fileInfo      // Keys: document uri
	defs  map[string]*defInfo       // Keys: symbol key
	refs  map[string]*refResultInfo // Keys: symbol key

	// Monikers
	packageName           string
	packageVersion        string
	packageInformationIDs map[string]string
}

// NewIndexer creates a new Indexer.
func NewIndexer(
	projectRoot string,
	toolInfo protocol.ToolInfo,
	w io.Writer,
) Indexer {
	return &indexer{
		projectRoot: projectRoot,
		toolInfo:    toolInfo,
		w:           protocol.NewWriter(w, true),

		// Empty maps
		files:                 map[string]*fileInfo{},
		defs:                  map[string]*defInfo{},
		refs:                  map[string]*refResultInfo{},
		packageInformationIDs: map[string]string{},
	}
}

// Index generates an LSIF dump from a SemanticDB dump by processing each
// file and writing the LSIF equivalent to the output source that implements
// io.Writer. It is caller's responsibility to close the output source if
// applicable.
func (i *indexer) Index() (*Stats, error) {
	err := i.loadDatabases()
	if err != nil {
		return nil, err
	}

	return i.index()
}

func (i *indexer) loadDatabases() error {
	log.Infoln("Loading semanticdb data...")

	err := filepath.Walk(i.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".semanticdb") {
			if err := i.loadDatabase(path); err != nil {
				return fmt.Errorf("load database %s: %v", path, err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("load databases: %v", err)
	}

	return nil
}

func (i *indexer) loadDatabase(path string) error {
	contents, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	textDocuments := &pb.TextDocuments{}
	if err := proto.Unmarshal(contents, textDocuments); err != nil {
		return err
	}

	for _, document := range textDocuments.GetDocuments() {
		symbols := map[string]*pb.SymbolInformation{}
		for _, symbol := range document.GetSymbols() {
			key := symbol.GetSymbol()
			if _, ok := symbols[key]; ok {
				return fmt.Errorf("duplicate symbol: %s", key)
			}
			symbols[key] = symbol
		}

		i.files[document.GetUri()] = &fileInfo{
			document: document,
			symbols:  symbols,
		}
	}

	return nil
}

func (i *indexer) index() (*Stats, error) {
	// TODO - how to get real URI?
	realURI, err := filepath.Abs(".")
	if err != nil {
		return nil, fmt.Errorf("get abspath of project root: %v", err)
	}

	_, err = i.w.EmitMetaData("file://"+realURI, i.toolInfo)
	if err != nil {
		return nil, fmt.Errorf(`emit "metadata": %v`, err)
	}
	proID, err := i.w.EmitProject(LanguageScala)
	if err != nil {
		return nil, fmt.Errorf(`emit "project": %v`, err)
	}

	if err := i.indexDbDocs(proID); err != nil {
		return nil, fmt.Errorf("index documents: %v", err)
	}

	for uri, fi := range i.files {
		if err := i.indexDbDefs(uri, fi, proID); err != nil {
			return nil, fmt.Errorf("index defs: %v", err)
		}
	}

	for uri, fi := range i.files {
		if err := i.indexDbUses(uri, fi, proID); err != nil {
			return nil, fmt.Errorf("index uses: %v", err)
		}
	}

	log.Infoln("Linking references...")

	for _, fi := range i.files {
		for _, occurrence := range fi.document.GetOccurrences() {
			if occurrence.GetRole() != pb.SymbolOccurrence_DEFINITION {
				continue
			}

			key := occurrence.GetSymbol()

			refResultID, err := i.w.EmitReferenceResult()
			if err != nil {
				return nil, fmt.Errorf(`emit "referenceResult": %v`, err)
			}

			_, err = i.w.EmitTextDocumentReferences(i.refs[key].resultSetID, refResultID)
			if err != nil {
				return nil, fmt.Errorf(`emit "textDocument/references": %v`, err)
			}

			for docID, rangeIDs := range i.refs[key].defRangeIDs {
				_, err = i.w.EmitItemOfDefinitions(refResultID, rangeIDs, docID)
				if err != nil {
					return nil, fmt.Errorf(`emit "item": %v`, err)
				}
			}

			for docID, rangeIDs := range i.refs[key].refRangeIDs {
				_, err = i.w.EmitItemOfReferences(refResultID, rangeIDs, docID)
				if err != nil {
					return nil, fmt.Errorf(`emit "item": %v`, err)
				}
			}
		}

		if len(fi.defRangeIDs) > 0 || len(fi.useRangeIDs) > 0 {
			// Deduplicate ranges before emitting a contains edge
			union := map[string]bool{}
			for _, id := range fi.defRangeIDs {
				union[id] = true
			}
			for _, id := range fi.useRangeIDs {
				union[id] = true
			}
			allRanges := []string{}
			for id := range union {
				allRanges = append(allRanges, id)
			}

			_, err = i.w.EmitContains(fi.docID, allRanges)
			if err != nil {
				return nil, fmt.Errorf(`emit "contains": %v`, err)
			}
		}
	}

	return &Stats{}, nil
}

func (i *indexer) indexDbDocs(proID string) (err error) {
	log.Infoln("Emitting documents...")

	for uri, fi := range i.files {
		// TODO - how to get real URI?
		realURI, err := filepath.Abs(uri)
		if err != nil {
			return fmt.Errorf("get abspath of document uri: %v", err)
		}

		docID, err := i.w.EmitDocument(LanguageScala, realURI)
		if err != nil {
			return fmt.Errorf(`emit "document": %v`, err)
		}

		_, err = i.w.EmitContains(proID, []string{docID})
		if err != nil {
			return fmt.Errorf(`emit "contains": %v`, err)
		}

		fi.docID = docID
	}

	return nil
}

func (i *indexer) indexDbDefs(uri string, fi *fileInfo, proID string) (err error) {
	log.Infoln("Emitting definitions for", uri)
	defer log.Infoln()

	var rangeIDs []string
	for _, occurrence := range fi.document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_DEFINITION {
			continue
		}

		key := occurrence.GetSymbol()
		symbol := fi.symbols[key]

		rangeID, err := i.w.EmitRange(convertRange(occurrence.GetRange()))
		if err != nil {
			return fmt.Errorf(`emit "range": %v`, err)
		}
		rangeIDs = append(rangeIDs, rangeID)

		refResult, ok := i.refs[key]
		if !ok {
			resultSetID, err := i.w.EmitResultSet()
			if err != nil {
				return fmt.Errorf(`emit "resultSet": %v`, err)
			}

			refResult = &refResultInfo{
				resultSetID: resultSetID,
				defRangeIDs: map[string][]string{},
				refRangeIDs: map[string][]string{},
			}

			i.refs[key] = refResult
		}

		if _, ok := refResult.defRangeIDs[fi.docID]; !ok {
			refResult.defRangeIDs[fi.docID] = []string{}
		}
		refResult.defRangeIDs[fi.docID] = append(refResult.defRangeIDs[fi.docID], rangeID)

		_, err = i.w.EmitNext(rangeID, refResult.resultSetID)
		if err != nil {
			return fmt.Errorf(`emit "next": %v`, err)
		}

		defResultID, err := i.w.EmitDefinitionResult()
		if err != nil {
			return fmt.Errorf(`emit "definitionResult": %v`, err)
		}

		_, err = i.w.EmitTextDocumentDefinition(refResult.resultSetID, defResultID)
		if err != nil {
			return fmt.Errorf(`emit "textDocument/definition": %v`, err)
		}

		_, err = i.w.EmitItem(defResultID, []string{rangeID}, fi.docID)
		if err != nil {
			return fmt.Errorf(`emit "item": %v`, err)
		}

		i.defs[key] = &defInfo{
			docID:       fi.docID,
			rangeID:     rangeID,
			resultSetID: refResult.resultSetID,
			defResultID: defResultID,
		}

		// TODO - add moniker support
		// TODO - only if public
		// err = i.emitExportMoniker(refResult.resultSetID, key) // TODO - better moniker
		// if err != nil {
		// 	return fmt.Errorf(`emit moniker": %v`, err)
		// }

		contents := []protocol.MarkedString{
			{
				Language: "scala",
				Value:    symbol.GetDisplayName(), // TODO - construct better text
			},
		}

		hoverResultID, err := i.w.EmitHoverResult(contents)
		if err != nil {
			return fmt.Errorf(`emit "hoverResult": %v`, err)
		}

		_, err = i.w.EmitTextDocumentHover(refResult.resultSetID, hoverResultID)
		if err != nil {
			return fmt.Errorf(`emit "textDocument/hover": %v`, err)
		}

		rangeIDs = append(rangeIDs, rangeID)
	}

	fi.defRangeIDs = append(fi.defRangeIDs, rangeIDs...)
	return nil
}

func (i *indexer) indexDbUses(uri string, fi *fileInfo, proID string) (err error) {
	log.Infoln("Emitting uses for", uri)
	defer log.Infoln()

	var rangeIDs []string
	for _, occurrence := range fi.document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_REFERENCE {
			continue
		}

		key := occurrence.GetSymbol()
		keys := []string{key}
		// TODO - temporary hack to support field assignment
		keys = append(keys, strings.Replace(strings.Replace(key, "_=", "", -1), "`", "", -1))

		var def *defInfo
		var defKey string = ""
		for _, k := range keys {
			tmp, ok := i.defs[k]
			if ok {
				def = tmp
				defKey = k
				break
			}
		}

		rangeID, err := i.w.EmitRange(convertRange(occurrence.GetRange()))
		if err != nil {
			return fmt.Errorf(`emit "range": %v`, err)
		}
		rangeIDs = append(rangeIDs, rangeID)

		if def == nil {
			// TODO - add moniker support
			// If we don't have a definition in this package, emit an import moniker
			// so that we can correlate it with another dump's LSIF data.
			// err = i.emitImportMoniker(rangeID, key)
			// if err != nil {
			// 	return fmt.Errorf(`emit moniker": %v`, err)
			// }

			// Emit a reference result edge and create a small set of edges that link
			// the reference result to the range (and vice versa). This is necessary to
			// mark this range as a reference to _something_, even though the definition
			// does not exist in this source code.

			refResultID, err := i.w.EmitReferenceResult()
			if err != nil {
				return fmt.Errorf(`emit "referenceResult": %v`, err)
			}

			_, err = i.w.EmitTextDocumentReferences(rangeID, refResultID)
			if err != nil {
				return fmt.Errorf(`emit "textDocument/references": %v`, err)
			}

			_, err = i.w.EmitItemOfReferences(refResultID, []string{rangeID}, fi.docID)
			if err != nil {
				return fmt.Errorf(`emit "item": %v`, err)
			}

			continue
		}

		_, err = i.w.EmitNext(rangeID, def.resultSetID)
		if err != nil {
			return fmt.Errorf(`emit "next": %v`, err)
		}

		refResult := i.refs[defKey]
		if refResult != nil {
			if _, ok := refResult.refRangeIDs[fi.docID]; !ok {
				refResult.refRangeIDs[fi.docID] = []string{}
			}
			refResult.refRangeIDs[fi.docID] = append(refResult.refRangeIDs[fi.docID], rangeID)
		}
	}

	fi.useRangeIDs = append(fi.useRangeIDs, rangeIDs...)
	return nil
}

// func (i *indexer) ensurePackageInformation(packageName, version string) (string, error) {
// 	packageInformationID, ok := i.packageInformationIDs[packageName]
// 	if !ok {
// 		var err error
// 		packageInformationID, err = i.w.EmitPackageInformation(packageName, "TODO", version)
// 		if err != nil {
// 			return "", err
// 		}

// 		i.packageInformationIDs[packageName] = packageInformationID
// 	}

// 	return packageInformationID, nil
// }

// func (i *indexer) emitImportMoniker(sourceID, identifier string) error {
// 	// TODO - not sure how to find this
// 	return nil
// }

// func (i *indexer) emitExportMoniker(sourceID, identifier string) error {
// 	packageInformationID, err := i.ensurePackageInformation(i.packageName, i.packageVersion)
// 	if err != nil {
// 		return err
// 	}

// 	return i.addMonikers("export", identifier, sourceID, packageInformationID)
// }

// func (i *indexer) addMonikers(kind string, identifier string, sourceID, packageID string) error {
// 	monikerID, err := i.w.EmitMoniker(kind, "TODO", identifier)
// 	if err != nil {
// 		return err
// 	}

// 	if _, err := i.w.EmitPackageInformationEdge(monikerID, packageID); err != nil {
// 		return err
// 	}

// 	if _, err := i.w.EmitMonikerEdge(sourceID, monikerID); err != nil {
// 		return err
// 	}

// 	return nil
// }
