package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/config"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
)

var version = "0.1.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "ownward:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("缺少命令")
	}
	if args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "Ownward 数据目录")
	content := flags.String("content", "", "信息内容")
	kindValue := flags.String("kind", string(domain.KindGeneral), "兼容既有资产的可选字段，不参与自主语义组织")
	id := flags.String("id", "", "信息标识")
	revision := flags.Uint64("revision", 0, "期望的信息版本")
	query := flags.String("query", "", "检索内容")
	limit := flags.Int("limit", 10, "最大返回数量")
	depth := flags.Int("depth", 1, "关系导航深度")
	output := flags.String("output", "", "备份输出文件")
	backup := flags.String("backup", "", "待恢复的备份文件")
	listen := flags.String("listen", "127.0.0.1:0", "Streamable HTTP MCP 监听地址，仅允许本机回环地址")
	token := flags.String("token", "", "Streamable HTTP MCP 可选的 Bearer Token")
	contexts := stringList{}
	flags.Var(&contexts, "context", "场景，格式为 key=value，可重复")
	relations := stringList{}
	flags.Var(&relations, "relation", "关系类型，可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	loaded, err := config.Load(*dataDir)
	if err != nil {
		return err
	}
	if command == "mcp" {
		return runSharedMCPConnector(ctx, loaded.DataDir, version, stdout, stderr)
	}
	bundle, bundleErr := loadEmbeddingBundle()
	if command == "restore" {
		if strings.TrimSpace(*backup) == "" {
			return errors.New("restore 必须提供 --backup")
		}
		if err := assetlog.Restore(*backup, filepath.Join(loaded.DataDir, "assets")); err != nil {
			return err
		}
	}
	store, err := assetlog.Open(filepath.Join(loaded.DataDir, "assets"))
	if err != nil {
		return err
	}
	derivedStore, err := derived.Open(filepath.Join(loaded.DataDir, "state"))
	if err != nil {
		_ = store.Close()
		return err
	}
	var vectorCapability embedding.Provider
	if bundleErr != nil {
		vectorCapability = embedding.Unavailable{Reason: "无法加载本地向量能力包: " + bundleErr.Error()}
	} else {
		managed, openErr := embedding.OpenManagedBundle(bundle)
		if openErr != nil {
			vectorCapability = embedding.Unavailable{Reason: "本地向量能力不可用: " + openErr.Error()}
		} else {
			vectorCapability = managed
		}
	}
	service, err := core.NewCollaborative(store, derivedStore, vectorCapability)
	if err != nil {
		_ = derivedStore.Close()
		_ = store.Close()
		return err
	}
	defer service.Close()
	parsedContexts, err := parseContexts(contexts)
	if err != nil {
		return err
	}
	switch command {
	case "mcp-http":
		debug.FreeOSMemory()
		resolvedToken := strings.TrimSpace(*token)
		if resolvedToken == "" {
			resolvedToken = strings.TrimSpace(os.Getenv(sharedMCPTokenEnvironment))
		}
		return runHTTPMCP(ctx, mcpserver.New(service, version), *listen, resolvedToken, stdout)
	case "create":
		kind, err := domain.ParseKind(*kindValue)
		if err != nil {
			return err
		}
		value, err := service.Create(ctx, core.CreateInput{Kind: kind, Content: *content, Contexts: parsedContexts, Source: domain.Source{Actor: "cli"}})
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "update":
		if *revision == 0 {
			return errors.New("update 必须提供 --revision")
		}
		input := core.UpdateInput{ID: *id, ExpectedRevision: *revision}
		if *content != "" {
			input.Content = content
		}
		if flagsWasSet(flags, "kind") {
			kind, err := domain.ParseKind(*kindValue)
			if err != nil {
				return err
			}
			input.Kind = &kind
		}
		if len(contexts) > 0 {
			input.Contexts = &parsedContexts
		}
		value, err := service.Update(ctx, input)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "read":
		value, err := service.Read(ctx, *id)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "search":
		values, err := service.Search(ctx, core.SearchInput{Query: *query, Contexts: parsedContexts, Limit: *limit})
		if err != nil {
			return err
		}
		return writeJSON(stdout, values)
	case "navigate":
		value, err := service.Navigate(ctx, []string{*id}, relations, *depth, *limit)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "rules":
		fmt.Fprint(stdout, service.Rules(ctx))
		return nil
	case "backup":
		if strings.TrimSpace(*output) == "" {
			return errors.New("backup 必须提供 --output")
		}
		if err := store.Backup(*output); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"backup": *output})
	case "restore":
		result, err := service.Maintain(ctx, true)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"restored": *backup, "organization": result})
	case "maintain", "rebuild":
		result, err := service.Maintain(ctx, command == "rebuild")
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	default:
		printUsage(stderr)
		return fmt.Errorf("未知命令 %q", command)
	}
}

func parseContexts(values []string) ([]domain.Context, error) {
	contexts := make([]domain.Context, 0, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("场景 %q 必须采用 key=value 格式", value)
		}
		contexts = append(contexts, domain.Context{Key: key, Value: item})
	}
	return contexts, nil
}

func flagsWasSet(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == name {
			found = true
		}
	})
	return found
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func loadEmbeddingBundle() (embedding.Bundle, error) {
	executable, err := os.Executable()
	if err != nil {
		return embedding.Bundle{}, fmt.Errorf("定位本地向量能力包: %w", err)
	}
	return embedding.LoadBundle(filepath.Join(filepath.Dir(executable), "embedding"))
}

type httpMCPServer interface {
	HTTPHandler() http.Handler
}

func runHTTPMCP(ctx context.Context, server httpMCPServer, address, token string, stdout io.Writer) error {
	listener, err := net.Listen("tcp", strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("启动 Streamable HTTP MCP: %w", err)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		_ = listener.Close()
		return errors.New("Streamable HTTP MCP 只允许监听本机回环地址")
	}
	shutdownRequested := make(chan struct{}, 1)
	baseHandler := server.HTTPHandler()
	var handler http.Handler = baseHandler
	if strings.TrimSpace(os.Getenv(sharedMCPDescriptorEnvironment)) != "" {
		handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case sharedMCPStatusPath:
				if request.Method != http.MethodGet {
					http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(writer).Encode(map[string]string{"service_identity": os.Getenv(sharedMCPIdentityEnvironment)})
			case sharedMCPShutdownPath:
				if request.Method != http.MethodPost {
					http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				writer.WriteHeader(http.StatusAccepted)
				select {
				case shutdownRequested <- struct{}{}:
				default:
				}
			default:
				baseHandler.ServeHTTP(writer, request)
			}
		})
	}
	if strings.TrimSpace(token) != "" {
		handler = bearerTokenHandler(handler, strings.TrimSpace(token))
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	endpoint := "http://" + net.JoinHostPort(tcpAddress.IP.String(), strconv.Itoa(tcpAddress.Port))
	cleanupDescriptor, err := publishSharedMCPDescriptorFromEnvironment(endpoint)
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer cleanupDescriptor()
	if err := writeJSON(stdout, map[string]string{"endpoint": endpoint}); err != nil {
		_ = listener.Close()
		return err
	}
	result := make(chan error, 1)
	go func() { result <- httpServer.Serve(listener) }()
	select {
	case <-ctx.Done():
	case <-shutdownRequested:
	case serveErr := <-result:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	serveErr := <-result
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func bearerTokenHandler(next http.Handler, token string) http.Handler {
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := []byte(request.Header.Get("Authorization"))
		if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "用法: ownward <mcp|mcp-http|create|update|read|search|navigate|rules|backup|restore|maintain|rebuild|version> [选项]")
	fmt.Fprintln(writer, "信息类型:", strings.Join(kindNames(), ", "))
}

func kindNames() []string {
	values := domain.Kinds()
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strconv.Quote(string(value)))
	}
	return result
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
