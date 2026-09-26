//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func main() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()
	imgs, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	var names []string
	for _, im := range imgs {
		for _, tag := range im.RepoTags {
			if strings.Contains(tag, "postgres") || strings.Contains(tag, "mysql") || strings.Contains(tag, "toxiproxy") || strings.Contains(tag, "agentsql-binder") {
				names = append(names, fmt.Sprintf("%s %s", tag, im.ID))
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
}
