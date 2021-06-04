// Copyright 2021 Zenauth Ltd.

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/local"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/cerbos/cerbos/internal/compile"
	"github.com/cerbos/cerbos/internal/engine"
	cerbosdevv1 "github.com/cerbos/cerbos/internal/genpb/cerbosdev/v1"
	responsev1 "github.com/cerbos/cerbos/internal/genpb/response/v1"
	svcv1 "github.com/cerbos/cerbos/internal/genpb/svc/v1"
	"github.com/cerbos/cerbos/internal/storage/disk"
	"github.com/cerbos/cerbos/internal/svc"
	"github.com/cerbos/cerbos/internal/test"
	"github.com/cerbos/cerbos/internal/util"
)

func TestServer(t *testing.T) {
	test.SkipIfGHActions(t) // TODO (cell) Servers don't work inside GH Actions for some reason.

	eng, cancelFunc := mkEngine(t)
	defer cancelFunc()

	cerbosSvc := svc.NewCerbosService(eng)

	t.Run("with_tls", func(t *testing.T) {
		testdataDir := test.PathToDir(t, "server")

		t.Run("tcp", func(t *testing.T) {
			conf := &Conf{
				HTTPListenAddr: getFreeListenAddr(t),
				GRPCListenAddr: getFreeListenAddr(t),
				TLS: &TLSConf{
					Cert: filepath.Join(testdataDir, "tls.crt"),
					Key:  filepath.Join(testdataDir, "tls.key"),
				},
				PlaygroundEnabled: true,
			}

			ctx, cancelFunc := context.WithCancel(context.Background())
			defer cancelFunc()

			startServer(ctx, conf, cerbosSvc)

			tlsConf := &tls.Config{InsecureSkipVerify: true} //nolint:gosec

			t.Run("grpc", testGRPCRequest(conf.GRPCListenAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf))))
			t.Run("grpc_over_http", testGRPCRequest(conf.HTTPListenAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf))))
			t.Run("http", testHTTPRequest(fmt.Sprintf("https://%s", conf.HTTPListenAddr)))
		})

		t.Run("uds", func(t *testing.T) {
			tempDir := t.TempDir()

			conf := &Conf{
				HTTPListenAddr: fmt.Sprintf("unix:%s", filepath.Join(tempDir, "http.sock")),
				GRPCListenAddr: fmt.Sprintf("unix:%s", filepath.Join(tempDir, "grpc.sock")),
				TLS: &TLSConf{
					Cert: filepath.Join(testdataDir, "tls.crt"),
					Key:  filepath.Join(testdataDir, "tls.key"),
				},
				PlaygroundEnabled: true,
			}

			ctx, cancelFunc := context.WithCancel(context.Background())
			defer cancelFunc()

			startServer(ctx, conf, cerbosSvc)

			tlsConf := &tls.Config{InsecureSkipVerify: true} //nolint:gosec

			t.Run("grpc", testGRPCRequest(conf.GRPCListenAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf))))
			t.Run("grpc_over_http", testGRPCRequest(conf.HTTPListenAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf))))
		})
	})

	t.Run("without_tls", func(t *testing.T) {
		t.Run("tcp", func(t *testing.T) {
			conf := &Conf{
				HTTPListenAddr:    getFreeListenAddr(t),
				GRPCListenAddr:    getFreeListenAddr(t),
				PlaygroundEnabled: true,
			}

			ctx, cancelFunc := context.WithCancel(context.Background())
			defer cancelFunc()

			startServer(ctx, conf, cerbosSvc)

			t.Run("grpc", testGRPCRequest(conf.GRPCListenAddr, grpc.WithTransportCredentials(local.NewCredentials())))
			t.Run("http", testHTTPRequest(fmt.Sprintf("http://%s", conf.HTTPListenAddr)))
		})

		t.Run("uds", func(t *testing.T) {
			tempDir := t.TempDir()

			conf := &Conf{
				HTTPListenAddr:    fmt.Sprintf("unix:%s", filepath.Join(tempDir, "http.sock")),
				GRPCListenAddr:    fmt.Sprintf("unix:%s", filepath.Join(tempDir, "grpc.sock")),
				PlaygroundEnabled: true,
			}

			ctx, cancelFunc := context.WithCancel(context.Background())
			defer cancelFunc()

			startServer(ctx, conf, cerbosSvc)

			t.Run("grpc", testGRPCRequest(conf.GRPCListenAddr, grpc.WithTransportCredentials(local.NewCredentials())))
		})
	})
}

func mkEngine(t *testing.T) (*engine.Engine, context.CancelFunc) {
	t.Helper()

	dir := test.PathToDir(t, "store")

	ctx, cancelFunc := context.WithCancel(context.Background())

	store, err := disk.NewStore(ctx, &disk.Conf{Directory: dir, ScratchDir: t.TempDir()})
	require.NoError(t, err)

	eng, err := engine.New(ctx, compile.NewCompiler(ctx, store))
	require.NoError(t, err)

	return eng, cancelFunc
}

func getFreeListenAddr(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err, "Failed to create listener")

	addr := lis.Addr().String()
	lis.Close()

	return addr
}

func startServer(ctx context.Context, conf *Conf, cerbosSvc *svc.CerbosService) {
	s := NewServer(conf)
	go func() {
		if err := s.Start(ctx, cerbosSvc, false); err != nil {
			panic(err)
		}
	}()
	runtime.Gosched()
}

func testGRPCRequest(addr string, opts ...grpc.DialOption) func(*testing.T) {
	//nolint:thelper
	return func(t *testing.T) {
		dialOpts := append(defaultGRPCDialOpts(), opts...)

		grpcConn, err := grpc.Dial(addr, dialOpts...)
		require.NoError(t, err, "Failed to dial gRPC server")

		cerbosClient := svcv1.NewCerbosServiceClient(grpcConn)
		playgroundClient := svcv1.NewCerbosPlaygroundServiceClient(grpcConn)

		testCases := test.LoadTestCases(t, filepath.Join("server", "requests"))

		for _, tcase := range testCases {
			tcase := tcase
			t.Run(tcase.Name, func(t *testing.T) {
				tc := readTestCase(t, tcase.Input)

				var have, want proto.Message
				var err error

				ctx, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancelFunc()

				switch call := tc.CallKind.(type) {
				case *cerbosdevv1.ServerTestCase_CheckResourceSet:
					want = call.CheckResourceSet.WantResponse
					have, err = cerbosClient.CheckResourceSet(ctx, call.CheckResourceSet.Input)
				case *cerbosdevv1.ServerTestCase_CheckResourceBatch:
					want = call.CheckResourceBatch.WantResponse
					have, err = cerbosClient.CheckResourceBatch(ctx, call.CheckResourceBatch.Input)
				case *cerbosdevv1.ServerTestCase_PlaygroundValidate:
					want = call.PlaygroundValidate.WantResponse
					have, err = playgroundClient.PlaygroundValidate(ctx, call.PlaygroundValidate.Input)
				case *cerbosdevv1.ServerTestCase_PlaygroundEvaluate:
					want = call.PlaygroundEvaluate.WantResponse
					have, err = playgroundClient.PlaygroundEvaluate(ctx, call.PlaygroundEvaluate.Input)
				default:
					t.Fatalf("Unknown call type: %T", call)
				}

				if tc.WantError {
					require.Error(t, err)
					return
				}

				require.NoError(t, err)
				compareProto(t, want, have)
			})
		}
	}
}

func testHTTPRequest(server string) func(*testing.T) {
	//nolint:thelper
	return func(t *testing.T) {
		customTransport := http.DefaultTransport.(*http.Transport).Clone()
		customTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec

		c := &http.Client{Transport: customTransport}

		testCases := test.LoadTestCases(t, filepath.Join("server", "requests"))

		for _, tcase := range testCases {
			tcase := tcase
			t.Run(tcase.Name, func(t *testing.T) {
				tc := readTestCase(t, tcase.Input)

				var input, have, want proto.Message
				var addr string

				switch call := tc.CallKind.(type) {
				case *cerbosdevv1.ServerTestCase_CheckResourceSet:
					addr = fmt.Sprintf("%s/api/check", server)
					input = call.CheckResourceSet.Input
					want = call.CheckResourceSet.WantResponse
					have = &responsev1.CheckResourceSetResponse{}
				case *cerbosdevv1.ServerTestCase_CheckResourceBatch:
					addr = fmt.Sprintf("%s/api/x/check_resource_batch", server)
					input = call.CheckResourceBatch.Input
					want = call.CheckResourceBatch.WantResponse
					have = &responsev1.CheckResourceBatchResponse{}
				case *cerbosdevv1.ServerTestCase_PlaygroundValidate:
					addr = fmt.Sprintf("%s/api/playground/validate", server)
					input = call.PlaygroundValidate.Input
					want = call.PlaygroundValidate.WantResponse
					have = &responsev1.PlaygroundValidateResponse{}
				case *cerbosdevv1.ServerTestCase_PlaygroundEvaluate:
					addr = fmt.Sprintf("%s/api/playground/evaluate", server)
					input = call.PlaygroundEvaluate.Input
					want = call.PlaygroundEvaluate.WantResponse
					have = &responsev1.PlaygroundEvaluateResponse{}
				default:
					t.Fatalf("Unknown call type: %T", call)
				}

				reqBytes, err := protojson.Marshal(input)
				require.NoError(t, err, "Failed to marshal request")

				ctx, cancelFunc := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancelFunc()

				req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(reqBytes))
				require.NoError(t, err, "Failed to create request")

				req.Header.Set("Content-Type", "application/json")

				resp, err := c.Do(req)
				require.NoError(t, err, "HTTP request failed")

				defer func() {
					if resp.Body != nil {
						resp.Body.Close()
					}
				}()

				if tc.WantError {
					require.NotEqual(t, http.StatusOK, resp.StatusCode)
					return
				}

				// require.Equal(t, http.StatusOK, resp.StatusCode)

				respBytes, err := io.ReadAll(resp.Body)
				require.NoError(t, err, "Failed to read response")

				require.NoError(t, protojson.Unmarshal(respBytes, have), "Failed to unmarshal response")
				compareProto(t, want, have)
			})
		}
	}
}

func readTestCase(t *testing.T, data []byte) *cerbosdevv1.ServerTestCase {
	t.Helper()

	tc := &cerbosdevv1.ServerTestCase{}
	require.NoError(t, util.ReadJSONOrYAML(bytes.NewReader(data), tc))

	return tc
}

func compareProto(t *testing.T, want, have interface{}) {
	t.Helper()

	require.Empty(t, cmp.Diff(want, have,
		protocmp.Transform(),
		protocmp.SortRepeated(cmpPlaygroundEvalResult),
		protocmp.SortRepeated(cmpPlaygroundError),
	))
}

func cmpPlaygroundEvalResult(a, b *responsev1.PlaygroundEvaluateResponse_EvalResult) bool {
	return a.Action < b.Action
}

func cmpPlaygroundError(a, b *responsev1.PlaygroundFailure_Error) bool {
	if a.File == b.File {
		return a.Error < b.Error
	}

	return a.File < b.File
}
