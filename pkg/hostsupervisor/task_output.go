package hostsupervisor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Read bounded durable output before sealing the receipt. A read failure leaves
// the stop unpublished so recovery can retry without losing the task result.
func readTaskResultOutput(ctx context.Context, reader substrate.ConnectionExecutionOutput, h substrate.ExecutionHandle, stream string) (string, bool, error) {
	var out bytes.Buffer
	var cursor int64
	for {
		limit := min(64<<10, substrate.DefaultExecOutputLimit-out.Len()+1)
		page, err := reader.ReadExecutionOutput(ctx, h, stream, cursor, limit)
		if err != nil {
			return "", false, err
		}
		if len(page.Data) > limit || page.NextCursor != cursor+int64(len(page.Data)) {
			return "", false, fmt.Errorf("invalid task output cursor")
		}
		out.Write(page.Data)
		cursor = page.NextCursor
		if out.Len() > substrate.DefaultExecOutputLimit {
			return string(out.Bytes()[:substrate.DefaultExecOutputLimit]), true, nil
		}
		if len(page.Data) < limit {
			return out.String(), false, nil
		}
	}
}
