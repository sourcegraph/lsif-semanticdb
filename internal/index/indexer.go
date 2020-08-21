package index

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/gogo/protobuf/proto"
	"github.com/pkg/errors"
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
	NumFiles    uint
	NumDefs     uint
	NumElements uint64
}

// indexer keeps track of all information needed to generate an LSIF dump.
type indexer struct {
	projectRoot       string
	printProgressDots bool
	toolInfo          protocol.ToolInfo
	w                 *protocol.Emitter

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
	printProgressDots bool,
	toolInfo protocol.ToolInfo,
	w io.Writer,
) Indexer {
	return &indexer{
		projectRoot:       projectRoot,
		printProgressDots: printProgressDots,
		toolInfo:          toolInfo,
		w:                 protocol.NewEmitter(NewJSONWriter(w)),

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
			document:  document,
			symbols:   symbols,
			localDefs: map[string]*defInfo{},
			localRefs: map[string]*refResultInfo{},
		}
	}

	return nil
}

func (i *indexer) index() (*Stats, error) {
	realURI, err := filepath.Abs(".")
	if err != nil {
		return nil, fmt.Errorf("get abspath of project root: %v", err)
	}

	_ = i.w.EmitMetaData("file://"+realURI, i.toolInfo)
	proID := i.w.EmitProject(LanguageScala)
	_ = i.indexDbDocs(proID)

	for uri, fi := range i.files {
		if i.printProgressDots {
			fmt.Fprintf(os.Stdout, ".")
		}

		_ = i.indexDbDefs(uri, fi, proID)
	}

	for uri, fi := range i.files {
		if i.printProgressDots {
			fmt.Fprintf(os.Stdout, ".")
		}

		_ = i.indexDbUses(uri, fi, proID)
	}

	log.Infoln("Linking references...")

	for _, fi := range i.files {
		if i.printProgressDots {
			fmt.Fprintf(os.Stdout, ".")
		}

		for _, occurrence := range fi.document.GetOccurrences() {
			if occurrence.GetRole() != pb.SymbolOccurrence_DEFINITION {
				continue
			}

			key := occurrence.GetSymbol()
			isLocal := strings.HasPrefix(key, "local")

			var refResultInfo *refResultInfo
			if isLocal {
				refResultInfo = fi.localRefs[key]
			} else {
				refResultInfo = i.refs[key]
			}

			if refResultInfo == nil {
				continue
			}

			refResultID := i.w.EmitReferenceResult()
			_ = i.w.EmitTextDocumentReferences(refResultInfo.resultSetID, refResultID)

			for docID, rangeIDs := range refResultInfo.defRangeIDs {
				_ = i.w.EmitItemOfDefinitions(refResultID, rangeIDs, docID)
			}

			for docID, rangeIDs := range refResultInfo.refRangeIDs {
				_ = i.w.EmitItemOfReferences(refResultID, rangeIDs, docID)
			}
		}

		if len(fi.defRangeIDs) > 0 || len(fi.useRangeIDs) > 0 {
			// Deduplicate ranges before emitting a contains edge
			union := map[uint64]bool{}
			for _, id := range fi.defRangeIDs {
				union[id] = true
			}
			for _, id := range fi.useRangeIDs {
				union[id] = true
			}
			allRanges := []uint64{}
			for id := range union {
				allRanges = append(allRanges, id)
			}

			_ = i.w.EmitContains(fi.docID, allRanges)
		}
	}

	numDefs := len(i.defs)
	for _, fi := range i.files {
		numDefs += len(fi.localDefs)
	}

	if err := i.w.Flush(); err != nil {
		return nil, errors.Wrap(err, "emitter.Flush")
	}

	return &Stats{
		NumFiles:    uint(len(i.files)),
		NumDefs:     uint(numDefs),
		NumElements: i.w.NumElements(),
	}, nil
}

func (i *indexer) indexDbDocs(proID uint64) error {
	log.Infoln("Emitting documents...")

	for uri, fi := range i.files {
		if i.printProgressDots {
			fmt.Fprintf(os.Stdout, ".")
		}

		realURI, err := filepath.Abs(uri)
		if err != nil {
			return fmt.Errorf("get abspath of document uri: %v", err)
		}

		docID := i.w.EmitDocument(LanguageScala, realURI)
		_ = i.w.EmitContains(proID, []uint64{docID})
		fi.docID = docID
	}

	return nil
}

func (i *indexer) indexDbDefs(uri string, fi *fileInfo, proID uint64) (err error) {
	log.Infoln("Emitting definitions for", uri)

	var rangeIDs []uint64
	for _, occurrence := range fi.document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_DEFINITION {
			continue
		}

		key := occurrence.GetSymbol()
		isLocal := strings.HasPrefix(key, "local")
		symbol := fi.symbols[key]

		rangeID := i.w.EmitRange(convertRange(occurrence.GetRange()))
		rangeIDs = append(rangeIDs, rangeID)

		var m map[string]*refResultInfo
		if isLocal {
			m = fi.localRefs
		} else {
			m = i.refs
		}

		refResult, ok := m[key]
		if !ok {
			resultSetID := i.w.EmitResultSet()

			refResult = &refResultInfo{
				resultSetID: resultSetID,
				defRangeIDs: map[uint64][]uint64{},
				refRangeIDs: map[uint64][]uint64{},
			}

			m[key] = refResult
		}

		if _, ok := refResult.defRangeIDs[fi.docID]; !ok {
			refResult.defRangeIDs[fi.docID] = []uint64{}
		}
		refResult.defRangeIDs[fi.docID] = append(refResult.defRangeIDs[fi.docID], rangeID)

		_ = i.w.EmitNext(rangeID, refResult.resultSetID)
		defResultID := i.w.EmitDefinitionResult()
		_ = i.w.EmitTextDocumentDefinition(refResult.resultSetID, defResultID)
		_ = i.w.EmitItem(defResultID, []uint64{rangeID}, fi.docID)

		def := &defInfo{
			docID:       fi.docID,
			rangeID:     rangeID,
			resultSetID: refResult.resultSetID,
			defResultID: defResultID,
		}

		if isLocal {
			fi.localDefs[key] = def
		} else {
			i.defs[key] = def
		}

		contents := []protocol.MarkedString{
			{
				Language: "scala",
				Value:    symbol.GetDisplayName(),
			},
		}

		hoverResultID := i.w.EmitHoverResult(contents)
		_ = i.w.EmitTextDocumentHover(refResult.resultSetID, hoverResultID)
		rangeIDs = append(rangeIDs, rangeID)
	}

	fi.defRangeIDs = append(fi.defRangeIDs, rangeIDs...)
	return nil
}

func (i *indexer) indexDbUses(uri string, fi *fileInfo, proID uint64) (err error) {
	log.Infoln("Emitting uses for", uri)

	var rangeIDs []uint64
	for _, occurrence := range fi.document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_REFERENCE {
			continue
		}

		def, refResult := i.getDefAndRefInfo(fi, occurrence.GetSymbol())

		rangeID := i.w.EmitRange(convertRange(occurrence.GetRange()))
		rangeIDs = append(rangeIDs, rangeID)

		if def == nil {
			refResultID := i.w.EmitReferenceResult()
			_ = i.w.EmitTextDocumentReferences(rangeID, refResultID)
			_ = i.w.EmitItemOfReferences(refResultID, []uint64{rangeID}, fi.docID)
			continue
		}

		_ = i.w.EmitNext(rangeID, def.resultSetID)

		if refResult != nil {
			if _, ok := refResult.refRangeIDs[fi.docID]; !ok {
				refResult.refRangeIDs[fi.docID] = []uint64{}
			}
			refResult.refRangeIDs[fi.docID] = append(refResult.refRangeIDs[fi.docID], rangeID)
		}
	}

	fi.useRangeIDs = append(fi.useRangeIDs, rangeIDs...)
	return nil
}

func (i *indexer) getDefAndRefInfo(fi *fileInfo, symbol string) (*defInfo, *refResultInfo) {
	def, ok := fi.localDefs[symbol]
	if ok {
		return def, fi.localRefs[symbol]
	}

	keys := []string{symbol}
	keys = append(keys, strings.Replace(symbol, ".", "#", -1))                               // pattern matching case class
	keys = append(keys, strings.Replace(strings.Replace(symbol, "_=", "", -1), "`", "", -1)) // field assignment

	for _, k := range keys {
		def, ok := i.defs[k]
		if ok {
			return def, i.refs[k]
		}
	}

	return nil, nil
}
