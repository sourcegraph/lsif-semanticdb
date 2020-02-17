package index

import pb "github.com/sourcegraph/lsif-semanticdb/internal/proto"

type fileInfo struct {
	document    *pb.TextDocument
	symbols     map[string]*pb.SymbolInformation
	docID       string
	defRangeIDs []string
	useRangeIDs []string
	localDefs   map[string]*defInfo
	localRefs   map[string]*refResultInfo
}

type defInfo struct {
	docID       string
	rangeID     string
	resultSetID string
	defResultID string
}

type refResultInfo struct {
	resultSetID string
	defRangeIDs map[string][]string
	refRangeIDs map[string][]string
}
