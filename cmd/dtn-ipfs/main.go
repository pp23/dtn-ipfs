package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	icore "github.com/ipfs/boxo/coreiface"
	icorepath "github.com/ipfs/boxo/coreiface/path"
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
	cfg.Ipns.UsePubsub.WithDefault(true)
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
		ExtraOpts: map[string]bool{
			"pubsub": true,
			"ipnsps": true,
		},
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

func main() {
	log.Print("Getting an IPFS node running")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ipfsA, nodeA, err := spawnEphemeral(ctx, true)
	if err != nil {
		log.Fatal(err)
	}
	log.Print("[nodeA] Identity: ", nodeA.Identity.Pretty())
	key, err := ipfsA.Key().Self(ctx)
	if err != nil {
		log.Panic(err)
	}
	log.Print("[nodeA] SelfKey: ", key.ID())

	if nodeA.PubSub == nil {
		log.Panic("PubSub is nil")
	}

	subIpfs, err := nodeA.PubSub.Subscribe("/ipns")
	if err != nil {
		log.Fatal(err)
	}
	log.Print("[nodeA] Subscribed Topics: ", nodeA.PubSub.GetTopics())
	go func() {
		for {
			msg, err := subIpfs.Next(ctx)
			if err != nil {
				log.Print(err)
				return
			}
			log.Print("[nodeA] Incoming message: ", msg.String())
		}
	}()

	rootPeerCidFile, err := ipfsA.Unixfs().Add(ctx, files.NewBytesFile([]byte("rootPeer data")))
	if err != nil {
		log.Panic(err)
	}

	log.Print("[nodeA] Added file with CID ", rootPeerCidFile.String())
	log.Print("[nodeA] Subscribed Topics: ", nodeA.PubSub.GetTopics())

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
				log.Print("[nodeA] Subscribed Topics: ", nodeA.PubSub.GetTopics())
				_, err := ipfsA.Unixfs().Add(ctx, files.NewBytesFile([]byte("rootPeer data 3")))
				if err != nil {
					log.Panic(err)
				}
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

	/*	ipfsBChan, err := ipfsB.Name().Search(ctx, "")
		if err != nil {
			log.Panic(err)
		}

		go func() {
			for {
				select {
				case ipnsResult := <-ipfsBChan:
					log.Print("[nodeB] ipnsResult: ", ipnsResult)
				}
			}
		}()
	*/
	go func() {
		nodeAIpnsName := "/ipns/" + nodeA.Identity.String()
		ipnsResult, err := nodeA.Namesys.Resolve(ctx, nodeAIpnsName)
		if err != nil {
			log.Panic(err)
		}
		log.Print("[nodeA] ", ipnsResult.String())
	}()

	subIpfsB, err := nodeB.PubSub.Subscribe("/ipfs")
	if err != nil {
		log.Fatal(err)
	}
	log.Print("[nodeB] Subscribed Topics: ", nodeB.PubSub.GetTopics())
	go func() {
		for {
			msg, err := subIpfsB.Next(ctx)
			if err != nil {
				log.Print(err)
				return
			}
			log.Print("[nodeB] Incoming message: ", msg.String())
		}
	}()

	rootPeerMa := "/ip4/127.0.0.1/tcp/4001/p2p/" + nodeA.Identity.String()

	bootstrapNodes := []string{
		rootPeerMa,
	}

	// ================== Get notified about new CIDs via Blockstore ==================================
	// Blockstore allows to get a chan for all keys, which are Cids.
	// Not sure, whether the cids are those which got added by nodeA
	// as the one cid we see is pretty empty after write out
	cidChan, err := nodeB.Blockstore.AllKeysChan(ctx)
	if err != nil {
		log.Panic(err)
	}

	// finds at least one cid, but after write out, the file is empty
	// furthermore, the channels first value is a valid cid, next cids string is only "b"...needs investigation
	go func() {
		for {
			select {
			case cid := <-cidChan:
				log.Print("[nodeB] Got new CID: ", cid.String(), " :: ", cid.KeyString())
				blocks, err := nodeB.Blockstore.Get(ctx, cid)
				if err != nil {
					log.Panic(err)
				}
				log.Print("[nodeB] cid string: ", blocks.String())
				fNode, err := ipfsB.Dag().Get(ctx, cid)
				if err != nil {
					log.Panic(err)
				}
				log.Print("[nodeB] node data: ", fNode.String())
				cidPath := icorepath.New(cid.String())
				cidNode, err := ipfsB.Unixfs().Get(ctx, cidPath)
				if err != nil {
					log.Panic(err)
				}
				log.Print("[nodeB] node data: ", cidNode)
				err = files.WriteTo(cidNode, "/tmp/test")
				if err != nil {
					log.Panic(err)
				}
				break
			default:
			}
			break
		}
	}()
	// ================================================================================================

	go func() {
		err := connectToPeers(ctx, ipfsB, bootstrapNodes)
		if err != nil {
			log.Print("[nodeB] failed connecto to peers: ", err)
		}
		sub, err := ipfsB.PubSub().Subscribe(ctx, "/ipns/"+nodeA.Identity.String())
		if err != nil {
			log.Panic(err)
		}
		go func() {
			for {
				msg, err := sub.Next(ctx)
				if err != nil {
					log.Panic(err)
				}
				log.Print("[nodeB] ", msg.From(), msg.Data())
			}
		}()
		log.Print("[nodeB] Subscribed Topics: ", nodeB.PubSub.GetTopics())
		f2, err2 := ipfsA.Unixfs().Add(ctx, files.NewBytesFile([]byte("rootPeer data 4")))
		if err2 != nil {
			log.Panic(err)
		}
		log.Print("[nodeA] Added file ", f2.String())
	}()

	<-ctx.Done()
}
