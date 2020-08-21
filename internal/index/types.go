package index

import pb "github.com/sourcegraph/lsif-semanticdb/internal/proto"

type fileInfo struct {
	document    *pb.TextDocument
	symbols     map[string]*pb.SymbolInformation
	docID       uint64
	defRangeIDs []uint64
	useRangeIDs []uint64
	localDefs   map[string]*defInfo
	localRefs   map[string]*refResultInfo
}

type defInfo struct {
	docID       uint64
	rangeID     uint64
	resultSetID uint64
	defResultID uint64
}

type refResultInfo struct {
	resultSetID uint64
	defRangeIDs map[uint64][]uint64
	refRangeIDs map[uint64][]uint64
}
