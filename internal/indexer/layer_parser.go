package indexer

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type ExtensionMetrics struct {
	Folder     string
	Extension  string
	TotalLOC   int
	TokenCount int
	FileCount  int
}

type WorkspaceState struct {
	sync.RWMutex
	Topology  map[string]*ExtensionMetrics
	FileSizes map[string]int64             
}

var CurrentState = &WorkspaceState{
	Topology:  make(map[string]*ExtensionMetrics),
	FileSizes: make(map[string]int64),
}

func StartBackgroundIndex(directories []string) {
	var wg sync.WaitGroup
	
	for _, dir := range directories {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			scanDirectory(path)
		}(dir)
	}
	
	wg.Wait()
}

func scanDirectory(root string) {
	cleanRoot := filepath.Clean(root)

	_ = filepath.WalkDir(cleanRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil 
		}

		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" || name == ".next" || name == "__pycache__" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".go" || ext == ".py" || ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" || ext == ".sql" || ext == ".json" || ext == ".md" {
			info, err := d.Info()
			if err != nil {
				return nil
			}

			CurrentState.Lock()
			lastSize, exists := CurrentState.FileSizes[path]
			CurrentState.Unlock()

			if exists && lastSize == info.Size() {
				return nil
			}

			parseDynamicMetrics(cleanRoot, path, ext, info.Size())
		}
		return nil
	})
}

func parseDynamicMetrics(root, path, ext string, size int64) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	var loc int
	var charCount int
	
	reader := bufio.NewReader(file)
	buf := make([]byte, 4096)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			charCount += n
			for i := 0; i < n; i++ {
				if buf[i] == '\n' {
					loc++
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
	}

	estimatedTokens := charCount / 4
	folderNode := extractTopLevelFolder(root, path)
	compositeKey := folderNode + "|" + ext

	CurrentState.Lock()
	defer CurrentState.Unlock()

	CurrentState.FileSizes[path] = size

	metrics, exists := CurrentState.Topology[compositeKey]
	if !exists {
		metrics = &ExtensionMetrics{
			Folder:    folderNode,
			Extension: ext,
		}
		CurrentState.Topology[compositeKey] = metrics
	}

	metrics.TotalLOC += loc
	metrics.TokenCount += estimatedTokens
	metrics.FileCount++
}

func extractTopLevelFolder(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "root"
	}

	segments := strings.Split(filepath.ToSlash(rel), "/")
	if len(segments) <= 1 {
		return "root"
	}
	
	return segments[0]
}
