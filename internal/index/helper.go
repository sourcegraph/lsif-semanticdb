package index

import (
	"github.com/sourcegraph/lsif-go/protocol"
	pb "github.com/sourcegraph/lsif-semanticdb/internal/proto"
)

func convertRange(r *pb.Range) (start protocol.Pos, end protocol.Pos) {
	return protocol.Pos{
			Line:      int(r.StartLine),
			Character: int(r.StartCharacter),
		}, protocol.Pos{
			Line:      int(r.EndLine),
			Character: int(r.EndCharacter),
		}
}
