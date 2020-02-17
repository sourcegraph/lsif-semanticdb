package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/gogo/protobuf/proto"
	"github.com/sourcegraph/lsif-go/protocol"
	pb "github.com/sourcegraph/lsif-semanticdb/cmd/lsif-semanticdb/proto"
)

const LanguageScala = "scala"

type Indexer interface {
	Index() (*Stats, error)
}

type Stats struct {
	// TODO - add processing stats
}

type indexer struct {
	projectRoot string
	// printProgressDots bool
	toolInfo protocol.ToolInfo
	w        *protocol.Writer

	// Monikers
	packageName           string
	packageVersion        string
	packageInformationIDs map[string]string
}

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
		packageInformationIDs: map[string]string{},
	}
}

func (e *indexer) Index() (*Stats, error) {
	_, err := e.w.EmitMetaData("file://"+e.projectRoot, e.toolInfo)
	if err != nil {
		return nil, fmt.Errorf(`emit "metadata": %v`, err)
	}
	proID, err := e.w.EmitProject(LanguageScala)
	if err != nil {
		return nil, fmt.Errorf(`emit "project": %v`, err)
	}

	return e.indexDocuments(proID)
}

func (e *indexer) indexDocuments(proID string) (*Stats, error) {
	dumpPath := path.Join(e.projectRoot, "META-INF/semanticdb")

	files, err := ioutil.ReadDir(dumpPath)
	if err != nil {
		panic(err.Error())
	}

	for _, file := range files {
		contents, err := ioutil.ReadFile(path.Join(dumpPath, file.Name()))
		if err != nil {
			panic(err.Error())
		}

		documents := &pb.TextDocuments{}
		if err := proto.Unmarshal(contents, documents); err != nil {
			panic(err.Error())
		}

		for _, document := range documents.GetDocuments() {
			if err := e.indexDocument(proID, document); err != nil {
				return nil, err
			}
		}
	}

	return &Stats{}, nil
}

func (e *indexer) indexDocument(proID string, document *pb.TextDocument) error {
	docID, err := e.w.EmitDocument(LanguageScala, path.Join(e.projectRoot, document.GetUri()))
	if err != nil {
		return fmt.Errorf(`emit "document": %v`, err)
	}

	_, err = e.w.EmitContains(proID, []string{docID})
	if err != nil {
		return fmt.Errorf(`emit "contains": %v`, err)
	}

	symbols := map[string]*pb.SymbolInformation{}
	for _, symbol := range document.GetSymbols() {
		key := symbol.GetSymbol()
		if _, ok := symbols[key]; ok {
			return fmt.Errorf("duplicate symbol: %s", key)
		}
		symbols[key] = symbol
	}

	rangeIDs := []string{}
	refResults := map[string]*struct {
		resultSetID string
		defResultID string
		defIDs      []string
		refIDs      []string
	}{}

	for _, occurrence := range document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_DEFINITION {
			continue
		}

		key := occurrence.GetSymbol()
		symbol := symbols[key]

		refResult, ok := refResults[key]
		if !ok {
			resultSetID, err := e.w.EmitResultSet()
			if err != nil {
				return fmt.Errorf(`emit "resultSet": %v`, err)
			}

			refResult = &struct {
				resultSetID string
				defResultID string
				defIDs      []string
				refIDs      []string
			}{
				resultSetID: resultSetID,
				defIDs:      []string{},
				refIDs:      []string{},
			}

			refResults[key] = refResult
		}

		rangeID, err := e.w.EmitRange(convertRange(occurrence.GetRange()))
		if err != nil {
			return fmt.Errorf(`emit "range": %v`, err)
		}

		rangeIDs = append(rangeIDs, rangeID)
		refResult.defIDs = append(refResult.defIDs, rangeID)

		_, err = e.w.EmitNext(rangeID, refResult.resultSetID)
		if err != nil {
			return fmt.Errorf(`emit "next": %v`, err)
		}

		hoverResultID, err := e.w.EmitHoverResult([]protocol.MarkedString{
			{
				Language: "scala",
				Value:    symbol.GetDisplayName(), // TODO - construct better text
			},
		})
		if err != nil {
			return fmt.Errorf(`emit "hoverResult": %v`, err)
		}

		_, err = e.w.EmitTextDocumentHover(refResult.resultSetID, hoverResultID)
		if err != nil {
			return fmt.Errorf(`emit "textDocument/hover": %v`, err)
		}

		// TODO - only if public
		err = e.emitExportMoniker(refResult.resultSetID, key) // TODO - better moniker
		if err != nil {
			return fmt.Errorf(`emit moniker": %v`, err)
		}
	}

	for _, occurrence := range document.GetOccurrences() {
		if occurrence.GetRole() != pb.SymbolOccurrence_REFERENCE {
			continue
		}

		rangeID, err := e.w.EmitRange(convertRange(occurrence.GetRange()))
		if err != nil {
			return fmt.Errorf(`emit "range": %v`, err)
		}

		rangeIDs = append(rangeIDs, rangeID)

		//
		//

		key := occurrence.GetSymbol()
		refResult, ok := refResults[key]
		if !ok {
			nextKey := strings.Replace(strings.Replace(key, "_=", "", -1), "`", "", -1)
			refResult, ok = refResults[nextKey]
			if !ok {
				//
				// TODO - should also read all files in the package
				//

				// If we don't have a definition in this package, emit an import moniker
				// so that we can correlate it with another dump's LSIF data.
				err = e.emitImportMoniker(rangeID, key)
				if err != nil {
					return fmt.Errorf(`emit moniker": %v`, err)
				}

				// Emit a reference result edge and create a small set of edges that link
				// the reference result to the range (and vice versa). This is necessary to
				// mark this range as a reference to _something_, even though the definition
				// does not exist in this source code.

				refResultID, err := e.w.EmitReferenceResult()
				if err != nil {
					return fmt.Errorf(`emit "referenceResult": %v`, err)
				}

				_, err = e.w.EmitTextDocumentReferences(rangeID, refResultID)
				if err != nil {
					return fmt.Errorf(`emit "textDocument/references": %v`, err)
				}

				_, err = e.w.EmitItemOfReferences(refResultID, []string{rangeID}, docID)
				if err != nil {
					return fmt.Errorf(`emit "item": %v`, err)
				}

				continue
			}
		}

		refResult.refIDs = append(refResult.refIDs, rangeID)

		_, err = e.w.EmitNext(rangeID, refResult.resultSetID)
		if err != nil {
			return fmt.Errorf(`emit "next": %v`, err)
		}

		//
		//
		//

		if refResult.defResultID == "" {
			defResultID, err := e.w.EmitDefinitionResult()
			if err != nil {
				return fmt.Errorf(`emit "definitionResult": %v`, err)
			}

			_, err = e.w.EmitTextDocumentDefinition(refResult.resultSetID, defResultID)
			if err != nil {
				return fmt.Errorf(`emit "textDocument/definition": %v`, err)
			}

			refResult.defResultID = defResultID

			_, err = e.w.EmitItem(refResult.defResultID, refResult.defIDs, docID)
			if err != nil {
				return fmt.Errorf(`emit "item": %v`, err)
			}
		}

	}

	for _, refResult := range refResults {
		refResultID, err := e.w.EmitReferenceResult()
		if err != nil {
			return fmt.Errorf(`emit "referenceResult": %v`, err)
		}

		_, err = e.w.EmitTextDocumentReferences(refResult.resultSetID, refResultID)
		if err != nil {
			return fmt.Errorf(`emit "textDocument/references": %v`, err)
		}

		if len(refResult.defIDs) > 0 {
			_, err = e.w.EmitItemOfDefinitions(refResultID, refResult.defIDs, docID)
			if err != nil {
				return fmt.Errorf(`emit "item": %v`, err)
			}
		}

		if len(refResult.refIDs) > 0 {
			_, err = e.w.EmitItemOfReferences(refResultID, refResult.refIDs, docID)
			if err != nil {
				return fmt.Errorf(`emit "item": %v`, err)
			}
		}
	}

	_, err = e.w.EmitContains(docID, rangeIDs)
	if err != nil {
		return fmt.Errorf(`emit "contains": %v`, err)
	}

	return nil
}

func convertRange(r *pb.Range) (start protocol.Pos, end protocol.Pos) {
	return protocol.Pos{
			Line:      int(r.StartLine),
			Character: int(r.StartCharacter),
		}, protocol.Pos{
			Line:      int(r.EndLine),
			Character: int(r.EndCharacter),
		}
}

//
//
//

func (i *indexer) ensurePackageInformation(packageName, version string) (string, error) {
	packageInformationID, ok := i.packageInformationIDs[packageName]
	if !ok {
		var err error
		packageInformationID, err = i.w.EmitPackageInformation(packageName, "TODO", version)
		if err != nil {
			return "", err
		}

		i.packageInformationIDs[packageName] = packageInformationID
	}

	return packageInformationID, nil
}

func (i *indexer) emitImportMoniker(sourceID, identifier string) error {
	// TODO - not sure how to find this
	return nil
}

func (i *indexer) emitExportMoniker(sourceID, identifier string) error {
	packageInformationID, err := i.ensurePackageInformation(i.packageName, i.packageVersion)
	if err != nil {
		return err
	}

	return i.addMonikers("export", identifier, sourceID, packageInformationID)
}

func (i *indexer) addMonikers(kind string, identifier string, sourceID, packageID string) error {
	monikerID, err := i.w.EmitMoniker(kind, "TODO", identifier)
	if err != nil {
		return err
	}

	if _, err := i.w.EmitPackageInformationEdge(monikerID, packageID); err != nil {
		return err
	}

	if _, err := i.w.EmitMonikerEdge(sourceID, monikerID); err != nil {
		return err
	}

	return nil
}

//
// POC Driver

func main() {
	file, err := os.Create("dump.lsif")
	if err != nil {
		panic(err.Error())
	}
	defer file.Close()

	indexer := NewIndexer(
		"/Users/efritz/dev/efritz/waddle/src/common/",
		protocol.ToolInfo{
			Name:    "lsif-semanticdb",
			Version: "0.0.0",
		},
		file,
	)

	if _, err := indexer.Index(); err != nil {
		panic(err.Error())
	}
}
