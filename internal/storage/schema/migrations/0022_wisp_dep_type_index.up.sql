CREATE INDEX idx_wisp_dep_type ON wisp_dependencies (type);
CREATE INDEX idx_wisp_dep_type_depends ON wisp_dependencies (type, depends_on_id);
