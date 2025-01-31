package grpcServer

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/alexey-petrov/go-webauthn/gen/auth/v1/authv1connect"
	"github.com/alexey-petrov/go-webauthn/gen/db/v1/dbv1connect"
	auth "github.com/alexey-petrov/go-webauthn/internal/authService"
	"github.com/alexey-petrov/go-webauthn/internal/jwtService"
	"github.com/alexey-petrov/go-webauthn/internal/keys"
	"github.com/alexey-petrov/go-webauthn/internal/storage"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func NewAuthInterceptor() connect.UnaryInterceptorFunc {
	interceptor := func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(
			ctx context.Context,
			req connect.AnyRequest,
		) (connect.AnyResponse, error) {

			return next(ctx, req)
		})
	}
	return connect.UnaryInterceptorFunc(interceptor)
}

type GrpcServer struct {
	mux             *http.ServeMux
	path            string
	corsHandler     http.Handler
	srv             *http.Server
	shutDownTimeout time.Duration
	logger          *slog.Logger
}

func New(logger *slog.Logger, userService *storage.UserService, jwtService *jwtService.JwtServiceStore, erdtreeClient dbv1connect.ErdtreeStoreClient) (*GrpcServer, error) {
	publicUrl := os.Getenv("PUBLIC_URL")
	erdTreeUrl := os.Getenv("ERDTREE_URL")
	port := os.Getenv("PORT")

	if erdTreeUrl == "" {
		erdTreeUrl = os.Getenv("ERDTREE_LOCAL_URL")
	}

	auth := auth.NewExternalClient(erdTreeUrl, userService, jwtService, erdtreeClient)

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(NewAuthInterceptor())
	path, handler := authv1connect.NewAuthServiceHandler(auth, interceptors)

	corsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicUrl != "" {
			w.Header().Set("Access-Control-Allow-Origin", publicUrl)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", os.Getenv("CLIENT_URL"))
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Set-Cookie, connect-protocol-version")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			return
		}

		ctx := context.WithValue(r.Context(), keys.HttpRequestKey, keys.HttpRequestResponse{Request: r, Response: w})
		// Call the next handler with the updated context
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	fmt.Println("SERVING AT: ", "0.0.0.0:"+port)
	srv := &http.Server{
		Addr:    "0.0.0.0:" + port,
		Handler: h2c.NewHandler(mux, &http2.Server{}),
	}
	shutDownTimeout := 10 * time.Second

	return &GrpcServer{
		mux,
		path,
		corsHandler,
		srv,
		shutDownTimeout,
		logger,
	}, nil
}

func (server *GrpcServer) Run(ctx context.Context) error {
	errResult := make(chan error)
	go func() {
		server.mux.Handle(server.path, server.corsHandler)
		fmt.Println("LISTEN AND SERVE")

		// TODO: Fix <nil> logger
		// server.logger.Info(fmt.Sprintf("starting listening: %s", server.srv.Addr))

		// if server.certFilePath != "" && server.keyFilePath != "" {
		// 	errResult <- server.srv.ListenAndServeTLS(server.certFilePath, server.keyFilePath)
		// }
		server.srv.ListenAndServe()
	}()

	var err error
	select {
	case <-ctx.Done():
		return ctx.Err()

	case err = <-errResult:
	}
	return err
}

func (server *GrpcServer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), server.shutDownTimeout)
	defer cancel()
	err := server.srv.Shutdown(ctx)
	if err != nil {
		server.logger.Error("failed to shutdown HTTP Server", slog.String("error", err.Error()))
	}
}
