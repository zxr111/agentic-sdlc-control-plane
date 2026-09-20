CREATE TABLE IF NOT EXISTS ontology_entity_types (
    type_key VARCHAR(64) COLLATE "C" PRIMARY KEY,
    display_name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ontology_relation_types (
    type_key VARCHAR(64) COLLATE "C" PRIMARY KEY,
    from_type VARCHAR(64) COLLATE "C" NOT NULL REFERENCES ontology_entity_types(type_key),
    to_type VARCHAR(64) COLLATE "C" NOT NULL REFERENCES ontology_entity_types(type_key),
    display_name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ontology_entities (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    entity_type VARCHAR(64) COLLATE "C" NOT NULL REFERENCES ontology_entity_types(type_key),
    canonical_key VARCHAR(512) COLLATE "C" NOT NULL,
    canonical_name TEXT NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    revision INTEGER NOT NULL DEFAULT 1,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', canonical_name || ' ' || canonical_key)) STORED,
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, entity_type, canonical_key)
);

CREATE TABLE IF NOT EXISTS ontology_entity_revisions (
    id UUID PRIMARY KEY,
    entity_id UUID NOT NULL REFERENCES ontology_entities(id),
    revision INTEGER NOT NULL,
    canonical_name TEXT NOT NULL,
    attributes JSONB NOT NULL,
    status VARCHAR(24) COLLATE "C" NOT NULL,
    source_type VARCHAR(32) COLLATE "C" NOT NULL,
    source_id VARCHAR(128) COLLATE "C" NOT NULL,
    source_version VARCHAR(128) COLLATE "C" NOT NULL,
    source_hash VARCHAR(64) COLLATE "C" NOT NULL,
    authority_level INTEGER NOT NULL CHECK(authority_level BETWEEN 0 AND 100),
    valid_from TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid_to TIMESTAMPTZ(6),
    UNIQUE(entity_id, revision)
);

CREATE TABLE IF NOT EXISTS ontology_relations (
    id UUID PRIMARY KEY,
    project_id BIGINT NOT NULL,
    relation_type VARCHAR(64) COLLATE "C" NOT NULL REFERENCES ontology_relation_types(type_key),
    from_entity_id UUID NOT NULL REFERENCES ontology_entities(id),
    to_entity_id UUID NOT NULL REFERENCES ontology_entities(id),
    status VARCHAR(24) COLLATE "C" NOT NULL DEFAULT 'ACTIVE',
    confidence NUMERIC(5,4) NOT NULL DEFAULT 1 CHECK(confidence BETWEEN 0 AND 1),
    authority VARCHAR(24) COLLATE "C" NOT NULL,
    source_type VARCHAR(32) COLLATE "C" NOT NULL,
    source_id VARCHAR(128) COLLATE "C" NOT NULL,
    source_version VARCHAR(128) COLLATE "C" NOT NULL,
    source_hash VARCHAR(64) COLLATE "C" NOT NULL,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    valid_from TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid_to TIMESTAMPTZ(6),
    created_at TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id,relation_type,from_entity_id,to_entity_id,source_type,source_id,source_version)
);

CREATE INDEX IF NOT EXISTS idx_ontology_entities_search ON ontology_entities USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_ontology_relations_from ON ontology_relations(project_id,from_entity_id,status);
CREATE INDEX IF NOT EXISTS idx_ontology_relations_to ON ontology_relations(project_id,to_entity_id,status);

INSERT INTO ontology_entity_types(type_key,display_name) VALUES
('SERVICE','Service'),('CAPABILITY','Capability'),('FEATURE','Feature'),('COMPONENT','Component'),
('API','API'),('DATA_ENTITY','Data Entity'),('REPOSITORY','Repository'),('CODE_PATH','Code Path'),
('REQUIREMENT','Requirement'),('ACCEPTANCE_CRITERION','Acceptance Criterion'),('WORK_ITEM','Work Item'),
('ARTIFACT','Artifact'),('MERGE_REQUEST','Merge Request'),('COMMIT','Commit'),('DEPLOYMENT','Deployment')
ON CONFLICT DO NOTHING;

INSERT INTO ontology_relation_types(type_key,from_type,to_type,display_name) VALUES
('STORED_IN','SERVICE','REPOSITORY','stored in'),('CHANGES','WORK_ITEM','SERVICE','changes'),
('IMPLEMENTS_FEATURE','WORK_ITEM','FEATURE','implements feature'),('HAS_COMPONENT','SERVICE','COMPONENT','has component'),
('EXPOSES','SERVICE','API','exposes'),('OWNS_DATA','SERVICE','DATA_ENTITY','owns data'),
('LOCATED_AT','SERVICE','CODE_PATH','located at'),('VERIFIED_BY','FEATURE','ACCEPTANCE_CRITERION','verified by'),
('MR_IMPLEMENTS','MERGE_REQUEST','WORK_ITEM','implements'),('COMMIT_PART_OF','COMMIT','MERGE_REQUEST','part of'),
('MODIFIES','COMMIT','CODE_PATH','modifies')
ON CONFLICT DO NOTHING;
