// +build tools

package tools

import (
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"
	_ "mvdan.cc/gofumpt"
	_ "github.com/vektra/mockery/v2"
)