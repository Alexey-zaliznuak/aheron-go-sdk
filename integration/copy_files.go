package integration

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

type ImportCopyFileHandler func(context.Context, schemetransfer.ImportCopyFileRequest) (schemetransfer.ImportCopyFileResponse, error)

// HandleImportCopyFile serves the manifest's ImportCopyFilePath. Unlike other
// copy callbacks this writes an integration library entry. The domain callback
// must implement the durable receipt semantics of ImportCopyFileRequest; this
// HTTP wrapper only authenticates and validates the exchange.
func (v *Verifier) HandleImportCopyFile(guard CopyVersionGuard, fn ImportCopyFileHandler) http.Handler {
	return v.handleCopy(schemetransfer.ImportCopyFile, guard, func(ctx context.Context, raw []byte) (any, error) {
		if fn == nil {
			return nil, ErrCopyUnavailable
		}
		var req schemetransfer.ImportCopyFileRequest
		_ = json.Unmarshal(raw, &req)
		return fn(ctx, req)
	})
}
