package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/containerd/containerd"
	"github.com/gorilla/mux"
	lru "github.com/hashicorp/golang-lru"
	bootstrap "github.com/openfaas/faas-provider"
	"github.com/openfaas/faas-provider/httputil"
	"github.com/openfaas/faas-provider/logs"
	"github.com/openfaas/faas-provider/types"

	//"github.com/openfaas/faas-provider/types"
	"net/url"
	"time"

	"github.com/openfaas/faasd/pkg/cninetwork"
	faasdlogs "github.com/openfaas/faasd/pkg/logs"
	"github.com/openfaas/faasd/pkg/provider/config"
	"github.com/openfaas/faasd/pkg/provider/handlers"
	pb "github.com/openfaas/faasd/proto/agent"
	"google.golang.org/grpc"

	//"github.com/spf13/cobra"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/openfaas/faasd/cmd"
)

// These values will be injected into these variables at the build time.
var (
	// GitCommit Git Commit SHA
	GitCommit string
	// Version version of the CLI
	Version string
)

const (
	//address     = "localhost:50051"
	//defaultName = "world"
	defaultContentType    = "text/plain"
	MaxCacheItem          = 2
	MaxAgentFunctionCache = 5
	MaxClientLoad         = 6
	UseCache              = false
	UseLoadBalancerCache  = true
)

type Agent struct {
	Id      uint
	Address string
	Loads   uint
}

var ageantAddresses []Agent
var ageantLoad []uint

//var Cache *cache.Cache
var Cache *lru.Cache
var CacheAgent *lru.Cache
var mutex sync.Mutex
var mutexAgent sync.Mutex
var cacheHit uint
var cacheMiss uint
var loadMiss uint64

func main() {
	cacheHit = 0
	cacheMiss = 0
	loadMiss = 0

	Cache, _ = lru.New(MaxCacheItem)
	CacheAgent, _ = lru.New(MaxAgentFunctionCache)
	ageantAddresses = append(ageantAddresses, Agent{Id: 0, Address: "localhost:50051"})
	ageantAddresses = append(ageantAddresses, Agent{Id: 1, Address: "localhost:50052"})
	ageantAddresses = append(ageantAddresses, Agent{Id: 2, Address: "localhost:50053"})
	ageantAddresses = append(ageantAddresses, Agent{Id: 3, Address: "localhost:50054"})
	// i:=0
	// for {
	// 	ageantLoad = append(ageantLoad, uint(i))
	// 	i++
	// }
	//Cache = cache.New(5*time.Minute, 10*time.Minute)
	//Cache.ItemCount()
	fmt.Println("Mohammad First code")
	err := runProvider()
	if err != nil {
		fmt.Println("runProvider error:", err.Error())
	}
	//if _, ok := os.LookupEnv("CONTAINER_ID"); ok {
	//	fmt.Println("Mohammad CONTAINER_ID is exist")
	//	collect := cmd.RootCommand()
	//	collect.SetArgs([]string{"collect"})
	//	collect.SilenceUsage = true
	//	collect.SilenceErrors = true
	//	err := collect.Execute()
	//	if err != nil {
	//		fmt.Fprintf(os.Stderr, err.Error())
	//		os.Exit(1)
	//	}
	//	os.Exit(0)
	//}
	//
	//if err := cmd.Execute(Version, GitCommit); err != nil {
	//	os.Exit(1)
	//}

}

func runProvider() error {

	//pullPolicy, flagErr := command.Flags().GetString("pull-policy")
	//if flagErr != nil {
	//	return flagErr
	//}
	//
	//alwaysPull := false
	//if pullPolicy == "Always" {
	//	alwaysPull = true
	//}

	alwaysPull := true

	config, providerConfig, err := config.ReadFromEnv(types.OsEnv{})
	if err != nil {
		return err
	}

	log.Printf("faasd-provider starting..\tService Timeout: %s\n", config.WriteTimeout.String())
	log.Println("UseCache:", UseCache, " UseLoadBalancerCache: :", UseLoadBalancerCache)
	//printVersion()

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	writeHostsErr := ioutil.WriteFile(path.Join(wd, "hosts"),
		[]byte(`127.0.0.1	localhost`), cmd.WorkingDirectoryPermission)

	if writeHostsErr != nil {
		return fmt.Errorf("cannot write hosts file: %s", writeHostsErr)
	}

	writeResolvErr := ioutil.WriteFile(path.Join(wd, "resolv.conf"),
		[]byte(`nameserver 8.8.8.8`), cmd.WorkingDirectoryPermission)

	if writeResolvErr != nil {
		return fmt.Errorf("cannot write resolv.conf file: %s", writeResolvErr)
	}

	cni, err := cninetwork.InitNetwork()
	if err != nil {
		return err
	}

	client, err := containerd.New(providerConfig.Sock)
	if err != nil {
		return err
	}

	defer client.Close()
	invokeResolver := handlers.NewInvokeResolver(client)

	userSecretPath := path.Join(wd, "secrets")
	// proxy.NewHandlerFunc(*config, invokeResolver),
	bootstrapHandlers := types.FaaSHandlers{
		FunctionProxy:        NewHandlerFunc(*config, invokeResolver),
		DeleteHandler:        handlers.MakeDeleteHandler(client, cni),
		DeployHandler:        handlers.MakeDeployHandler(client, cni, userSecretPath, alwaysPull),
		FunctionReader:       handlers.MakeReadHandler(client),
		ReplicaReader:        handlers.MakeReplicaReaderHandler(client),
		ReplicaUpdater:       handlers.MakeReplicaUpdateHandler(client, cni),
		UpdateHandler:        handlers.MakeUpdateHandler(client, cni, userSecretPath, alwaysPull),
		HealthHandler:        func(w http.ResponseWriter, r *http.Request) {},
		InfoHandler:          handlers.MakeInfoHandler(Version, GitCommit),
		ListNamespaceHandler: cmd.ListNamespaces(),
		SecretHandler:        handlers.MakeSecretHandler(client, userSecretPath),
		LogHandler:           logs.NewLogHandlerFunc(faasdlogs.New(), config.ReadTimeout),
	}

	log.Printf("Listening on TCP port: %d\n", *config.TCPPort)
	bootstrap.Serve(&bootstrapHandlers, config)
	return nil

}

type BaseURLResolver interface {
	Resolve(functionName string) (url.URL, error)
}

func NewHandlerFunc(config types.FaaSConfig, resolver BaseURLResolver) http.HandlerFunc {
	log.Println("Mohammad NewHandlerFunc")
	if resolver == nil {
		panic("NewHandlerFunc: empty proxy handler resolver, cannot be nil")
	}

	//proxyClient := proxy.NewProxyClientFromConfig(config)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			defer r.Body.Close()
		}

		switch r.Method {
		case http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodGet:

			pathVars := mux.Vars(r)
			functionName := pathVars["name"]
			if functionName == "" {
				httputil.Errorf(w, http.StatusBadRequest, "missing function name")
				return
			}

			exteraPath := pathVars["params"]

			bodyBytes, err := ioutil.ReadAll(r.Body)
			if err != nil {
				log.Println("read request bodey error :", err.Error())
			}
			log.Println("Mohammad RequestURI: ", r.RequestURI, ", inputs:", string(bodyBytes))
			r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
			sReqHash := hash(append([]byte(functionName), bodyBytes...))

			//*********** cache  ******************
			if UseCache {
				mutex.Lock()
				response, found := Cache.Get(sReqHash)
				mutex.Unlock()
				if found {
					log.Println("Mohammad founded in cache  functionName: ", functionName)
					res, err := unserializeReq(response.([]byte), r)
					if err != nil {
						log.Println("Mohammad unserialize res: ", err.Error())
						httputil.Errorf(w, http.StatusInternalServerError, "Can't unserialize res: %s.", functionName)
						return
					}

					clientHeader := w.Header()
					copyHeaders(clientHeader, &res.Header)
					w.Header().Set("Content-Type", getContentType(r.Header, res.Header))

					w.WriteHeader(res.StatusCode)
					io.Copy(w, res.Body)
					return
				}
			}

			sReq, err := captureRequestData(r)
			if err != nil {
				httputil.Errorf(w, http.StatusInternalServerError, "Can't captureRequestData for: %s.", functionName)
				return
			}

			//proxy.ProxyRequest(w, r, proxyClient, resolver)
			agentRes, err := loadBalancer(functionName, exteraPath, sReq)
			if err != nil {
				httputil.Errorf(w, http.StatusInternalServerError, "Can't reach service for: %s.", functionName)
				return
			}

			//log.Println("Mohammad add to cache sReqHash:", sReqHash)
			if UseCache {
				mutex.Lock()
				Cache.Add(sReqHash, agentRes.Response)
				mutex.Unlock()
			}

			res, err := unserializeReq(agentRes.Response, r)
			if err != nil {
				log.Println("Mohammad unserialize res: ", err.Error())
				httputil.Errorf(w, http.StatusInternalServerError, "Can't unserialize res: %s.", functionName)
				return
			}

			clientHeader := w.Header()
			copyHeaders(clientHeader, &res.Header)
			w.Header().Set("Content-Type", getContentType(r.Header, res.Header))

			w.WriteHeader(res.StatusCode)
			io.Copy(w, res.Body)

			//w.WriteHeader(http.StatusOK)
			//_, _ =w.Write(agentRes.Response)
			//io.Copy(w, r.Response)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
	//return proxy.NewHandlerFunc(config, resolver)
}

func loadBalancer(RequestURI string, exteraPath string, sReq []byte) (*pb.TaskResponse, error) {

	var agentId uint32
	if UseLoadBalancerCache {
		mutexAgent.Lock()
		value, found := CacheAgent.Get(RequestURI)
		mutexAgent.Unlock()
		if found {
			agentId = value.(uint32)
			if ageantAddresses[agentId].Loads < MaxClientLoad {
				mutexAgent.Lock()
				ageantAddresses[agentId].Loads++
				cacheHit++
				mutexAgent.Unlock()
				log.Printf("sendToAgent due to Cache cacheHit: %v, address: %v,  RequestURI :%s", cacheHit, ageantAddresses[agentId].Address, RequestURI)
				return sendToAgent(ageantAddresses[agentId].Address, RequestURI, exteraPath, sReq, agentId)
			}
			atomic.AddUint64(&loadMiss, 1)
		}
	}

	mutexAgent.Lock()
	for i := 0; i < len(ageantAddresses); i++ {
		agentId = uint32(rand.Int31n(int32(len(ageantAddresses))))
		if ageantAddresses[agentId].Loads < MaxClientLoad {
			break
		}
	}
	if UseLoadBalancerCache {
		CacheAgent.Add(RequestURI, agentId)
		cacheMiss++
	}
	ageantAddresses[agentId].Loads++
	log.Printf("sendToAgent loadMiss: %v, cacheMiss: %v, address: %v,  RequestURI :%s", loadMiss, cacheMiss, ageantAddresses[agentId].Address, RequestURI)
	mutexAgent.Unlock()
	return sendToAgent(ageantAddresses[agentId].Address, RequestURI, exteraPath, sReq, agentId)

}

func sendToAgent(address string, RequestURI string, exteraPath string, sReq []byte, agentId uint32) (*pb.TaskResponse, error) {
	// log.Printf("sendToAgent address: %v,  RequestURI :%s", address, RequestURI)

	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		log.Printf("did not connect: %v", err)
		mutexAgent.Lock()
		ageantAddresses[agentId].Loads--
		mutexAgent.Unlock()
		return nil, err
	}
	defer conn.Close()
	c := pb.NewTasksRequestClient(conn)

	// Contact the server and print out its response.

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := c.TaskAssign(ctx, &pb.TaskRequest{FunctionName: RequestURI, ExteraPath: exteraPath, SerializeReq: sReq})
	if err != nil {
		log.Printf("could not TaskAssign: %v", err)
		mutexAgent.Lock()
		ageantAddresses[agentId].Loads--
		mutexAgent.Unlock()
		return nil, err
	}
	// log.Printf("Response Message: %s", r.Message)
	mutexAgent.Lock()
	ageantAddresses[agentId].Loads--
	mutexAgent.Unlock()
	return r, err

}

func captureRequestData(req *http.Request) ([]byte, error) {
	var b = &bytes.Buffer{} // holds serialized representation
	//var tmp *http.Request
	var err error
	if err = req.Write(b); err != nil { // serialize request to HTTP/1.1 wire format
		return nil, err
	}
	//var reqSerialize []byte

	return b.Bytes(), nil
	//r := bufio.NewReader(b)
	//if tmp, err = http.ReadRequest(r); err != nil { // deserialize request
	//	return nil,err
	//}
	//*req = *tmp // replace original request structure
	//return nil
}

func unserializeReq(sReq []byte, req *http.Request) (*http.Response, error) {
	b := bytes.NewBuffer(sReq)
	r := bufio.NewReader(b)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// copyHeaders clones the header values from the source into the destination.
func copyHeaders(destination http.Header, source *http.Header) {
	for k, v := range *source {
		vClone := make([]string, len(v))
		copy(vClone, v)
		destination[k] = vClone
	}
}

// getContentType resolves the correct Content-Type for a proxied function.
func getContentType(request http.Header, proxyResponse http.Header) (headerContentType string) {
	responseHeader := proxyResponse.Get("Content-Type")
	requestHeader := request.Get("Content-Type")

	if len(responseHeader) > 0 {
		headerContentType = responseHeader
	} else if len(requestHeader) > 0 {
		headerContentType = requestHeader
	} else {
		headerContentType = defaultContentType
	}

	return headerContentType
}

func hash(data []byte) string {
	h := sha1.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
