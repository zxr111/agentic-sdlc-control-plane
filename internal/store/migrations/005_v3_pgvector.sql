CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE knowledge_chunks ADD COLUMN IF NOT EXISTS embedding vector(64);

CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);
