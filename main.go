// Command connect-timelapse is a Prusa Connect Local module (runtime:
// service) that records a timelapse of every print: while a printer prints,
// it saves one camera frame every configured interval - pulled from the
// camera's RTSP stream with the server's ffmpeg, or copied out of the
// snapshot events the host already stores for pushed/HTTP cameras - and when
// the print ends it assembles the frames into one MP4 with ffmpeg. The last
// few prints are kept, configurable in the settings.
//
// It speaks the plugin wire protocol on stdin/stdout: the host launches it,
// and it answers plugin.info, plugin.configure, routes.serve and
// bus.subscribe calls, reads camera frames out of the server's storages and
// publishes <namespace>/timelapse.ready events.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"

	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const (
	name    = "connect-timelapse"
	release = extension.Version("0.3.0")
)

//go:embed openapi.json
var openAPI []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

// run writes the handshake line on stdout, then answers the host's calls until
// the host closes stdin.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return err
	}
	if _, err = os.Stdout.WriteString(line); err != nil {
		return err
	}

	serves := wirerpc.Methods{}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{Role: wirerpc.RoleChild, Methods: serves})
	if err != nil {
		return err
	}

	app := newApp()

	serves[wirerpc.MethodInfo] = func(context.Context, wirerpc.Call) (any, error) {
		return routesapi.Describe(wirerpc.Info{
			Name:     name,
			Version:  release,
			Contract: wirerpc.Contract,
			Serves:   []wirerpc.Method{routesapi.MethodServe, wirerpc.MethodConfigure},
		}, openAPI)
	}
	serves[wirerpc.MethodShutdown] = func(context.Context, wirerpc.Call) (any, error) {
		return nil, nil
	}
	serves[wirerpc.MethodConfigure] = wirerpc.ConfigureGranted(func(ctx context.Context, held heldSettings, grants wirerpc.Grants) error {
		return app.configure(ctx, held, grants.Data)
	})
	serves[routesapi.MethodServe] = routesapi.Serve(app.routes(conn))

	go app.watch(ctx, conn)

	return conn.Serve(ctx)
}

// stdio is the channel the host handed this process.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
