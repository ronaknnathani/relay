package programview

import (
	"sort"

	"github.com/ronaknnathani/relay/internal/program"
)

func graphDTO(items []program.WorkItem) GraphDTO {
	sortedItems := append([]program.WorkItem(nil), items...)
	sort.Slice(sortedItems, func(i, j int) bool {
		return numberedID(sortedItems[i].ID) < numberedID(sortedItems[j].ID)
	})
	byID := make(map[string]program.WorkItem, len(sortedItems))
	indegree := make(map[string]int, len(sortedItems))
	dependents := make(map[string][]string, len(sortedItems))
	graph := GraphDTO{
		Nodes:  make([]GraphNodeDTO, 0, len(sortedItems)),
		Edges:  []GraphEdgeDTO{},
		Layers: [][]string{},
	}
	for _, item := range sortedItems {
		byID[item.ID] = item
		indegree[item.ID] = 0
		dependents[item.ID] = []string{}
	}
	for _, item := range sortedItems {
		for _, dependency := range item.Dependencies {
			if _, exists := byID[dependency]; !exists {
				continue
			}
			indegree[item.ID]++
			dependents[dependency] = append(dependents[dependency], item.ID)
			graph.Edges = append(graph.Edges, GraphEdgeDTO{From: dependency, To: item.ID})
		}
	}
	for id := range dependents {
		sort.Slice(dependents[id], func(i, j int) bool {
			return numberedID(dependents[id][i]) < numberedID(dependents[id][j])
		})
	}
	sort.Slice(graph.Edges, func(i, j int) bool {
		if numberedID(graph.Edges[i].From) == numberedID(graph.Edges[j].From) {
			return numberedID(graph.Edges[i].To) < numberedID(graph.Edges[j].To)
		}
		return numberedID(graph.Edges[i].From) < numberedID(graph.Edges[j].From)
	})

	queue := make([]string, 0, len(sortedItems))
	for _, item := range sortedItems {
		if indegree[item.ID] == 0 {
			queue = append(queue, item.ID)
		}
	}
	layers := make(map[string]int, len(sortedItems))
	processed := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		processed++
		for _, dependent := range dependents[id] {
			if layers[dependent] < layers[id]+1 {
				layers[dependent] = layers[id] + 1
			}
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
				sort.Slice(queue, func(i, j int) bool {
					return numberedID(queue[i]) < numberedID(queue[j])
				})
			}
		}
	}
	graph.Cyclic = processed != len(sortedItems)
	for _, item := range sortedItems {
		layer := layers[item.ID]
		for len(graph.Layers) <= layer {
			graph.Layers = append(graph.Layers, []string{})
		}
		graph.Layers[layer] = append(graph.Layers[layer], item.ID)
		graph.Nodes = append(graph.Nodes, GraphNodeDTO{
			ID: item.ID, Title: item.Title, Lane: string(item.Status), Layer: layer,
		})
	}
	return graph
}
