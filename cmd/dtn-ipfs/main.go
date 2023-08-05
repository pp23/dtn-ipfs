package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	icore "github.com/ipfs/boxo/coreiface"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/node/libp2p"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
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

func createNode(ctx context.Context, repoPath string) (*core.IpfsNode, error) {
	repo, err := fsrepo.Open(repoPath)
	if err != nil {
		return nil, err
	}
	nodeOptions := &core.BuildCfg{
		Online:  true,
		Routing: libp2p.DHTOption,
		Repo:    repo,
	}
	return core.NewNode(ctx, nodeOptions)
}

func spawnEphemeral(ctx context.Context) (icore.CoreAPI, *core.IpfsNode, error) {
	repoPath, err := createTempRepo()
	if err != nil {
		return nil, nil, err
	}

	node, err := createNode(ctx, repoPath)
	if err != nil {
		return nil, nil, err
	}

	api, err := coreapi.NewCoreAPI(node)
	return api, node, err
}

func main() {
	log.Print("Getting an IPFS node running")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ipfsA, nodeA, err := spawnEphemeral(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Print(nodeA.Identity)
	log.Print(ipfsA.Name())
	var wait chan struct{}
	<-wait
}
