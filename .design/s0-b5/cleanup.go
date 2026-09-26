//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	containertype "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

const prefix = "agentsql-b5-s0-"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, e := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if e != nil {
		log.Fatal(e)
	}
	defer cli.Close()
	containers, e := cli.ContainerList(ctx, containertype.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agentsql.b5.s0=true"))})
	if e != nil {
		log.Fatal(e)
	}
	for _, c := range containers {
		safe := false
		for _, n := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(n, "/"), prefix) {
				safe = true
			}
		}
		if !safe {
			log.Fatalf("refusing labeled non-prefixed container %s %v", c.ID, c.Names)
		}
		fmt.Printf("remove container %s %v\n", c.ID[:12], c.Names)
		if e := cli.ContainerRemove(ctx, c.ID, containertype.RemoveOptions{Force: true, RemoveVolumes: true}); e != nil {
			log.Fatal(e)
		}
	}
	nets, e := cli.NetworkList(ctx, network.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	for _, n := range nets {
		if strings.HasPrefix(n.Name, prefix) {
			fmt.Printf("remove network %s\n", n.Name)
			if e := cli.NetworkRemove(ctx, n.ID); e != nil {
				log.Fatal(e)
			}
		}
	}
	vols, e := cli.VolumeList(ctx, volume.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	for _, v := range vols.Volumes {
		if strings.HasPrefix(v.Name, prefix) {
			fmt.Printf("remove volume %s\n", v.Name)
			if e := cli.VolumeRemove(ctx, v.Name, true); e != nil {
				log.Fatal(e)
			}
		}
	}
	imgs, e := cli.ImageList(ctx, image.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	for _, im := range imgs {
		for _, tag := range im.RepoTags {
			if strings.HasPrefix(tag, prefix) {
				fmt.Printf("remove image %s\n", tag)
				if _, e := cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: true, PruneChildren: true}); e != nil {
					log.Fatal(e)
				}
				break
			}
		}
	}
	containers, e = cli.ContainerList(ctx, containertype.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agentsql.b5.s0=true"))})
	if e != nil {
		log.Fatal(e)
	}
	nets, e = cli.NetworkList(ctx, network.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	vols, e = cli.VolumeList(ctx, volume.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	imgs, e = cli.ImageList(ctx, image.ListOptions{})
	if e != nil {
		log.Fatal(e)
	}
	leftN, leftV, leftI := 0, 0, 0
	for _, n := range nets {
		if strings.HasPrefix(n.Name, prefix) {
			leftN++
		}
	}
	for _, v := range vols.Volumes {
		if strings.HasPrefix(v.Name, prefix) {
			leftV++
		}
	}
	for _, im := range imgs {
		for _, tag := range im.RepoTags {
			if strings.HasPrefix(tag, prefix) {
				leftI++
			}
		}
	}
	fmt.Printf("audit containers=%d networks=%d volumes=%d images=%d\n", len(containers), leftN, leftV, leftI)
}
