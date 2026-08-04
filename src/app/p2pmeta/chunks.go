package p2pmeta

// ChunkManifest describes how an artifact is divided for swarm download.
type ChunkManifest struct {
	ArtifactID   string  `json:"artifactId"`
	ArtifactName string  `json:"artifactName"`
	TotalSize    int64   `json:"totalSize"`
	ChunkSize    int64   `json:"chunkSize"`
	TotalChunks  int     `json:"totalChunks"`
	SHA256       string  `json:"sha256"`
	SourceMTime  int64   `json:"sourceMtime"` // UnixNano do mtime do arquivo fonte
	Chunks       []Chunk `json:"chunks"`
}

// Chunk describes a single chunk within a manifest.
type Chunk struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
