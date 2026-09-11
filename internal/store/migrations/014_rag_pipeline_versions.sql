ALTER TABLE knowledge_versions
    ADD COLUMN IF NOT EXISTS normalized_content TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS normalized_hash CHAR(64) COLLATE "C" NOT NULL DEFAULT repeat('0', 64),
    ADD COLUMN IF NOT EXISTS parser_version VARCHAR(64) COLLATE "C" NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS cleaner_version VARCHAR(64) COLLATE "C" NOT NULL DEFAULT 'legacy';

ALTER TABLE knowledge_chunks
    ADD COLUMN IF NOT EXISTS chunker_version VARCHAR(64) COLLATE "C" NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS embedding_model VARCHAR(128) COLLATE "C" NOT NULL DEFAULT 'legacy';

CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding_model
    ON knowledge_chunks (embedding_model);

CREATE TABLE IF NOT EXISTS knowledge_chunk_embeddings (
    knowledge_chunk_id UUID NOT NULL REFERENCES knowledge_chunks(id) ON DELETE CASCADE,
    model_version VARCHAR(128) COLLATE "C" NOT NULL,
    dimensions INTEGER NOT NULL,
    input_hash CHAR(64) COLLATE "C" NOT NULL,
    embedding vector(64) NOT NULL,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (knowledge_chunk_id, model_version)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_chunk_embeddings_vector
    ON knowledge_chunk_embeddings USING hnsw (embedding vector_cosine_ops);

INSERT INTO knowledge_chunk_embeddings
    (knowledge_chunk_id,model_version,dimensions,input_hash,embedding)
SELECT id,embedding_model,64,content_hash,embedding
FROM knowledge_chunks
WHERE embedding IS NOT NULL
ON CONFLICT (knowledge_chunk_id,model_version) DO NOTHING;
