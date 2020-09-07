// The program lsif-semanticdb converts SemanticDB files into LSIF indexes.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kingpin"
	protocol "github.com/sourcegraph/lsif-protocol"
	"github.com/sourcegraph/lsif-semanticdb/internal/index"
	"github.com/sourcegraph/lsif-semanticdb/internal/log"
)

const version = "0.4.1"
const versionString = version + ", protocol version " + protocol.Version

func main() {
	if err := realMain(); err != nil {
		fmt.Fprint(os.Stderr, fmt.Sprintf("error: %v\n", err))
		os.Exit(1)
	}
}

func realMain() error {
	var (
		debug         bool
		verbose       bool
		semanticdbDir string
		noContents    bool
		outFile       string
	)

	app := kingpin.New("lsif-semanticdb", "lsif-semanticdb is an LSIF indexer for SemanticDB.").Version(versionString)
	app.Flag("debug", "Display debug information.").Default("false").BoolVar(&debug)
	app.Flag("verbose", "Display verbose information.").Short('v').Default("false").BoolVar(&verbose)
	app.Flag("semanticdbDir", "Specifies the directory of the META-INF/semanticdb directory.").Required().StringVar(&semanticdbDir)
	app.Flag("noContents", "File contents will not be embedded into the dump.").Default("false").BoolVar(&noContents)
	app.Flag("out", "The output file the dump is saved to.").Default("dump.lsif").StringVar(&outFile)

	_, err := app.Parse(os.Args[1:])
	if err != nil {
		return err
	}

	if verbose {
		log.SetLevel(log.Info)
	}

	if debug {
		log.SetLevel(log.Debug)
	}

	// Print progress dots if we have no other output
	printProgressDots := !verbose && !debug

	out, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("create dump file: %v", err)
	}

	defer out.Close()

	semanticdbDir, err = filepath.Abs(semanticdbDir)
	if err != nil {
		return fmt.Errorf("get abspath of SemanticDB dir: %v", err)
	}

	toolInfo := protocol.ToolInfo{
		Name:    "lsif-semanticdb",
		Version: version,
		Args:    os.Args[1:],
	}

	indexer := index.NewIndexer(
		semanticdbDir,
		// noContents,
		printProgressDots,
		toolInfo,
		out,
	)

	start := time.Now()
	s, err := indexer.Index()
	if printProgressDots {
		// End progress line before printing summary or error
		log.Println()
		log.Println()
	}

	if err != nil {
		return fmt.Errorf("index: %v", err)
	}

	log.Printf("%d file(s), %d def(s), %d element(s)", s.NumFiles, s.NumDefs, s.NumElements)
	log.Println("Processed in", time.Since(start))
	return nil
}
