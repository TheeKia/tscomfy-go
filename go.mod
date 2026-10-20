module github.com/TheeKia/tscomfy-go

go 1.27

// Skip every node_modules directory in ./... patterns (the workspace's and its members').
ignore node_modules

tool github.com/TheeKia/tscomfy-go/cmd/tscomfy-go

require github.com/coder/websocket v1.8.15
