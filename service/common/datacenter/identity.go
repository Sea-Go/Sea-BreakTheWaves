package datacenter

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
)

// FileTokenSource reads the deployment-managed DataCenter identity on every
// request so a sidecar can rotate it without restarting BTW.
func FileTokenSource(path string) TokenSource {
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if path == "" {
			return "", errors.New("DataCenter workload credential path is missing")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		value, err := io.ReadAll(io.LimitReader(file, 4097))
		if err != nil {
			return "", err
		}
		if len(value) > 4096 {
			return "", errors.New("DataCenter workload credential is too large")
		}
		token := strings.TrimSpace(string(value))
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return "", errors.New("DataCenter workload credential is invalid")
		}
		return token, nil
	}
}

func DeploymentIdentity() TokenSource {
	return FileTokenSource(strings.TrimSpace(os.Getenv("DATACENTER_WORKLOAD_TOKEN_FILE")))
}
