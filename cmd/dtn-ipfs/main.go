package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	icore "github.com/ipfs/boxo/coreiface"
	"github.com/ipfs/boxo/files"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/node/libp2p"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p/core/peer"
)

func setupPlugins(externalPluginsPath string) error {
	plugins, err := loader.NewPluginLoader(filepath.Join(externalPluginsPath, "plugins"))
	if err != nil {
		return err
	}
	if err := plugins.Initialize(); err != nil {
		return err
	}
	if err := plugins.Inject(); err != nil {
		return err
	}
	return nil
}

func createTempRepo() (string, error) {
	repoPath, err := os.MkdirTemp("", "ipfs-shell")
	if err != nil {
		return "", err
	}

	cfg, err := config.Init(io.Discard, 2048)
	if err != nil {
		return "", err
	}

	err = fsrepo.Init(repoPath, cfg)
	if err != nil {
		return "", err
	}

	return repoPath, nil
}

func createNode(ctx context.Context, repoPath string, online bool) (*core.IpfsNode, error) {
	repo, err := fsrepo.Open(repoPath)
	if err != nil {
		return nil, err
	}
	nodeOptions := &core.BuildCfg{
		Online:  online,
		Routing: libp2p.DHTOption,
		Repo:    repo,
	}
	return core.NewNode(ctx, nodeOptions)
}

var loadPluginsOnce sync.Once

func spawnEphemeral(ctx context.Context, online bool) (icore.CoreAPI, *core.IpfsNode, error) {
	var onceErr error
	loadPluginsOnce.Do(func() {
		onceErr = setupPlugins("")
	})
	if onceErr != nil {
		return nil, nil, onceErr
	}

	repoPath, err := createTempRepo()
	if err != nil {
		return nil, nil, err
	}

	node, err := createNode(ctx, repoPath, online)
	if err != nil {
		return nil, nil, err
	}

	api, err := coreapi.NewCoreAPI(node)
	return api, node, err
}

func connectToPeers(ctx context.Context, ipfs icore.CoreAPI, peers []string) error {
	var wg sync.WaitGroup
	peerInfos := make(map[peer.ID]*peer.AddrInfo, len(peers))
	for _, addrStr := range peers {
		addr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			return err
		}
		pii, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			return err
		}
		pi, ok := peerInfos[pii.ID]
		if !ok {
			pi = &peer.AddrInfo{ID: pii.ID}
			peerInfos[pi.ID] = pi
		}
		pi.Addrs = append(pi.Addrs, pii.Addrs...)
	}

	wg.Add(len(peerInfos))
	for _, peerInfo := range peerInfos {
		go func(peerInfo *peer.AddrInfo) {
			defer wg.Done()
			err := ipfs.Swarm().Connect(ctx, *peerInfo)
			if err != nil {
				log.Print("Failed to connecto to", peerInfo.ID, ": ", err)
				return
			}
			log.Print("Connected to ", peerInfo.ID)
		}(peerInfo)
	}
	wg.Wait()
	return nil
}

func main() {
	log.Print("Getting an IPFS node running")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ipfsA, nodeA, err := spawnEphemeral(ctx, true)
	if err != nil {
		log.Fatal(err)
	}
	log.Print(nodeA.Identity)

	rootPeerCidFile, err := ipfsA.Unixfs().Add(ctx, files.NewBytesFile([]byte("rootPeer data")))
	if err != nil {
		log.Panic(err)
	}

	log.Print("Added rootPeer file with CID ", rootPeerCidFile.String())

	ipfsB, _, err := spawnEphemeral(ctx, true)
	if err != nil {
		log.Panic(err)
	}

	rootPeerMa := "/ip4/127.0.0.1/tcp/4001/p2p/" + nodeA.Identity.String()

	bootstrapNodes := []string{
		rootPeerMa,
	}

	go func() {
		err := connectToPeers(ctx, ipfsB, bootstrapNodes)
		if err != nil {
			log.Print("failed connecto to peers: ", err)
		}
	}()

	var wait chan struct{}
	<-wait
}
