//go:build ignore

// Read-only cleanup audit for the S0 run. It lists containers whose image is
// one of the spike images; it never stops or removes anything.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func main() {
	remove := flag.String("remove", "", "comma-separated exact IDs to remove after label/image validation")
	flag.Parse()
	if os.Getenv("DOCKER_HOST") == "" {
		_ = os.Setenv("DOCKER_HOST", "npipe:////./pipe/docker_engine")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()
	rows, err := cli.ContainerList(context.Background(), containertypes.ListOptions{All: true})
	if err != nil {
		panic(err)
	}
	requested := map[string]bool{}
	for _, id := range strings.Split(*remove, ",") {
		if id != "" {
			requested[id] = false
		}
	}
	count := 0
	for _, row := range rows {
		if strings.HasPrefix(row.Image, "postgres:") || row.Image == "mysql:8" || strings.Contains(row.Image, "ryuk") {
			fmt.Printf("id=%s image=%s created=%d state=%s names=%v testcontainers=%s session=%s\n",
				row.ID[:12], row.Image, row.Created, row.State, row.Names,
				row.Labels["org.testcontainers"], row.Labels["org.testcontainers.session-id"])
			count++
		}
		for id := range requested {
			if strings.HasPrefix(row.ID, id) {
				if row.Labels["org.testcontainers"] != "true" || !(strings.HasPrefix(row.Image, "postgres:") || row.Image == "mysql:8") {
					panic("refusing non-spike container " + id)
				}
				if err := cli.ContainerRemove(context.Background(), row.ID, containertypes.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
					panic(err)
				}
				fmt.Printf("removed=%s image=%s names=%v\n", row.ID[:12], row.Image, row.Names)
				requested[id] = true
			}
		}
	}
	for id, done := range requested {
		if !done {
			panic("requested container not found: " + id)
		}
	}
	fmt.Printf("matching_containers=%d\n", count)
}
