module github.com/philip-soerensen/connect-local-timelapse

go 1.26.11

require (
	github.com/prusa3d/prusa-connect/sdk v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
