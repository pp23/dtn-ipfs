package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"

	icore "github.com/ipfs/boxo/coreiface"
	"github.com/ipfs/boxo/files"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/bootstrap"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/node/libp2p"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/libp2p/go-libp2p/core/event"
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
	// no initial peers for privacy, we will add our peers on demand
	cfg.SetBootstrapPeers([]peer.AddrInfo{})
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

	node, err := core.NewNode(ctx, nodeOptions)
	if err != nil {
		return node, err
	}
	// no initial peers for privacy, we will add our peers on demand
	bootstrapCfg := bootstrap.BootstrapConfigWithPeers([]peer.AddrInfo{})
	err = node.Bootstrap(bootstrapCfg)
	return node, err
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

func MultiAddrsToString(addrs []ma.Multiaddr) []string {
	var out []string
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	return out
}

func main() {
	log.Print("Getting an IPFS node running")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ipfsA, nodeA, err := spawnEphemeral(ctx, true)
	if err != nil {
		log.Fatal(err)
	}
	log.Print("[nodeA] Identity: ", nodeA.Identity)
	log.Print("[nodeA] Addresses: ", strings.Join(MultiAddrsToString(nodeA.PeerHost.Addrs()), "\n"))

	rootPeerCidFile, err := ipfsA.Unixfs().Add(ctx, files.NewBytesFile([]byte("rootPeer data")))
	if err != nil {
		log.Panic(err)
	}

	log.Print("[nodeA] Added file with CID ", rootPeerCidFile.String())

	sub, err := nodeA.PeerHost.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		log.Panic(err)
	}

	go func() {
		defer sub.Close()
		for e := range sub.Out() {
			switch e := e.(type) {
			case event.EvtPeerIdentificationCompleted:
				log.Print("[nodeA] Identification completed: ", e.Peer)
				log.Print("[nodeA] Peerstore: ", nodeA.Peerstore.Peers())
			default:
				log.Print("[nodeA] Unknown event type: ", e)
			}
		}
	}()

	var notifee network.NotifyBundle
	notifee.ConnectedF = func(n network.Network, c network.Conn) {
		log.Print("[nodeA] New connection from: ", c.RemotePeer())
	}
	nodeA.PeerHost.Network().Notify(&notifee)

	ipfsB, nodeB, err := spawnEphemeral(ctx, true)
	if err != nil {
		log.Panic(err)
	}
	log.Print("[nodeB] Identity: ", nodeB.Identity)

	rootPeerMa := "/ip4/127.0.0.1/tcp/4001/p2p/" + nodeA.Identity.String()

	bootstrapNodes := []string{
		rootPeerMa,
	}

	go func() {
		err := connectToPeers(ctx, ipfsB, bootstrapNodes)
		if err != nil {
			log.Print("[nodeB] failed connecto to peers: ", err)
		}
	}()

	<-ctx.Done()
}
