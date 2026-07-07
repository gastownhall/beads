package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestConfigSQLRepository() {
	s.Run("GetMetadata", func() {
		s.Run("MissingKeyReturnsEmpty", s.configGetMetadataMissingKey)
		s.Run("RoundTrip", s.configGetMetadataRoundTrip)
	})
	s.Run("SetMetadata", func() {
		s.Run("Overwrite", s.configSetMetadataOverwrite)
	})
	s.Run("SetLocalMetadata", func() {
		s.Run("WritesToLocalMetadataTable", s.configSetLocalMetadataWrites)
	})
	s.Run("GetLocalMetadata", func() {
		s.Run("MissingKeyReturnsEmpty", s.configGetLocalMetadataMissingKey)
		s.Run("RoundTrip", s.configGetLocalMetadataRoundTrip)
		s.Run("Overwrite", s.configGetLocalMetadataOverwrite)
		s.Run("ReadsFromLocalMetadataNotMetadata", s.configGetLocalMetadataIsolatedFromMetadata)
	})
	s.Run("GetConfig", func() {
		s.Run("MissingKeyReturnsEmpty", s.configGetConfigMissingKey)
		s.Run("RoundTrip", s.configGetConfigRoundTrip)
	})
	s.Run("SetConfig", func() {
		s.Run("Overwrite", s.configSetConfigOverwrite)
		s.Run("IssuePrefixTrimsTrailingHyphen", s.configSetConfigIssuePrefixTrim)
		s.Run("IssuePrefixWithoutHyphenUnchanged", s.configSetConfigIssuePrefixUnchanged)
	})
	s.Run("DeleteConfig", func() {
		s.Run("RemovesExistingKey", s.configDeleteConfigRemovesExisting)
		s.Run("MissingKeyIsNoop", s.configDeleteConfigMissingKey)
	})
	s.Run("GetAllConfig", func() {
		s.Run("EmptyReturnsEmptyMap", s.configGetAllConfigEmpty)
		s.Run("ReturnsAllRows", s.configGetAllConfigAllRows)
	})
	s.Run("ReconcileVersion", func() {
		s.Run("EmptyCliVersionErrors", s.configReconcileVersionEmptyErrors)
		s.Run("FreshInstallMigratesAndSetsMax", s.configReconcileVersionFreshInstall)
		s.Run("AlreadyCurrentIsNoop", s.configReconcileVersionAlreadyCurrent)
		s.Run("UpgradeAdvancesVersionAndMax", s.configReconcileVersionUpgrade)
		s.Run("DowngradeBelowCurrentRefused", s.configReconcileVersionDowngradeRefused)
		s.Run("DowngradeBelowMaxRefused", s.configReconcileVersionDowngradeBelowMaxRefused)
		s.Run("CatchUpToMaxMigrates", s.configReconcileVersionCatchUpToMax)
	})
	s.Run("UseCase", func() {
		s.Run("GetConfigMissingKey", s.configUseCaseGetConfigMissing)
		s.Run("GetConfigRoundTrip", s.configUseCaseGetConfigRoundTrip)
		s.Run("SetConfigOverwrite", s.configUseCaseSetConfigOverwrite)
		s.Run("SetConfigIssuePrefixTrim", s.configUseCaseSetConfigIssuePrefixTrim)
		s.Run("DeleteConfigRemovesExisting", s.configUseCaseDeleteConfigRemoves)
		s.Run("DeleteConfigMissingKeyIsNoop", s.configUseCaseDeleteConfigMissing)
		s.Run("GetAllConfigEmpty", s.configUseCaseGetAllConfigEmpty)
		s.Run("GetAllConfigReturnsAllRows", s.configUseCaseGetAllConfigAllRows)
	})
	s.Run("GetCustomTypes", func() {
		s.Run("MissingKeyReturnsNil", s.configGetCustomTypesMissing)
		s.Run("EmptyValueReturnsNil", s.configGetCustomTypesEmpty)
		s.Run("CommaSeparated", s.configGetCustomTypesCommaSeparated)
		s.Run("JSONArray", s.configGetCustomTypesJSONArray)
		s.Run("TrimsWhitespaceAndSkipsEmpty", s.configGetCustomTypesTrimsAndSkipsEmpty)
		s.Run("CustomTypesTableTakesPrecedenceOverConfigString", s.configGetCustomTypesTablePrecedence)
		s.Run("ConfigStringFallbackWhenTableEmpty", s.configGetCustomTypesConfigFallback)
	})
	s.Run("GetAllowedPrefixes", func() {
		s.Run("MissingKeyReturnsEmpty", s.configGetAllowedPrefixesMissing)
		s.Run("ReturnsRawValue", s.configGetAllowedPrefixesRawValue)
	})
	s.Run("GetAdaptiveIDConfig", func() {
		s.Run("MissingKeysReturnsDefaults", s.configGetAdaptiveIDConfigDefaults)
		s.Run("OverridesApplied", s.configGetAdaptiveIDConfigOverrides)
		s.Run("MalformedValuesFallBackToDefaults", s.configGetAdaptiveIDConfigMalformed)
	})
	s.Run("GetCustomStatuses", func() {
		s.Run("EmptyTableReturnsNil", s.configGetCustomStatusesEmpty)
		s.Run("ReturnsRowsOrderedByName", s.configGetCustomStatusesRows)
	})
	s.Run("ListAllStatusNames", func() {
		s.Run("BuiltinsOnlyWhenNoCustom", s.configListAllStatusNamesBuiltinsOnly)
		s.Run("BuiltinsFirstThenCustomAppended", s.configListAllStatusNamesAppendsCustom)
		s.Run("UseCaseSurfacesRepoResults", s.configUseCaseListAllStatusNames)
	})
	s.Run("GetInfraTypes", func() {
		s.Run("MissingKeyReturnsEmpty", s.configGetInfraTypesMissing)
		s.Run("EmptyValueReturnsEmpty", s.configGetInfraTypesEmpty)
		s.Run("CommaSeparated", s.configGetInfraTypesCommaSeparated)
		s.Run("TrimsWhitespaceAndSkipsEmpty", s.configGetInfraTypesTrimsAndSkipsEmpty)
	})
}

func (s *testSuite) configRepo() domain.ConfigSQLRepository {
	return NewConfigSQLRepository(s.Runner())
}

func (s *testSuite) configGetMetadataMissingKey() {
	v, err := s.configRepo().GetMetadata(s.Ctx(), "no_such_key")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configGetMetadataRoundTrip() {
	r := s.configRepo()
	s.Require().NoError(r.SetMetadata(s.Ctx(), "_project_id", "abc-123"))
	v, err := r.GetMetadata(s.Ctx(), "_project_id")
	s.Require().NoError(err)
	s.Equal("abc-123", v)
}

func (s *testSuite) configSetMetadataOverwrite() {
	r := s.configRepo()
	s.Require().NoError(r.SetMetadata(s.Ctx(), "k", "v1"))
	s.Require().NoError(r.SetMetadata(s.Ctx(), "k", "v2"))
	v, err := r.GetMetadata(s.Ctx(), "k")
	s.Require().NoError(err)
	s.Equal("v2", v)
}

func (s *testSuite) configSetLocalMetadataWrites() {
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.2.3"))

	var v string
	err := s.Runner().
		QueryRowContext(s.Ctx(), "SELECT value FROM local_metadata WHERE `key` = ?", "bd_version").
		Scan(&v)
	s.Require().NoError(err)
	s.Equal("1.2.3", v)
}

func (s *testSuite) configGetLocalMetadataMissingKey() {
	v, err := s.configRepo().GetLocalMetadata(s.Ctx(), "no_such_key")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configGetLocalMetadataRoundTrip() {
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.2.3"))
	v, err := r.GetLocalMetadata(s.Ctx(), "bd_version")
	s.Require().NoError(err)
	s.Equal("1.2.3", v)
}

func (s *testSuite) configGetLocalMetadataOverwrite() {
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.2.3"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.3.0"))
	v, err := r.GetLocalMetadata(s.Ctx(), "bd_version")
	s.Require().NoError(err)
	s.Equal("1.3.0", v)
}

func (s *testSuite) configGetLocalMetadataIsolatedFromMetadata() {
	r := s.configRepo()
	s.Require().NoError(r.SetMetadata(s.Ctx(), "shared_key", "from_metadata"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "shared_key", "from_local"))

	local, err := r.GetLocalMetadata(s.Ctx(), "shared_key")
	s.Require().NoError(err)
	s.Equal("from_local", local)

	global, err := r.GetMetadata(s.Ctx(), "shared_key")
	s.Require().NoError(err)
	s.Equal("from_metadata", global)
}

func (s *testSuite) configGetConfigMissingKey() {
	v, err := s.configRepo().GetConfig(s.Ctx(), "no_such_key")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configGetConfigRoundTrip() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "team.sync_branch", "main"))
	v, err := r.GetConfig(s.Ctx(), "team.sync_branch")
	s.Require().NoError(err)
	s.Equal("main", v)
}

func (s *testSuite) configSetConfigOverwrite() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "k", "v1"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "k", "v2"))
	v, err := r.GetConfig(s.Ctx(), "k")
	s.Require().NoError(err)
	s.Equal("v2", v)
}

func (s *testSuite) configSetConfigIssuePrefixTrim() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "issue_prefix", "bd-"))
	v, err := r.GetConfig(s.Ctx(), "issue_prefix")
	s.Require().NoError(err)
	s.Equal("bd", v)
}

func (s *testSuite) configDeleteConfigRemovesExisting() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "jira.url", "https://example.atlassian.net"))
	s.Require().NoError(r.DeleteConfig(s.Ctx(), "jira.url"))
	v, err := r.GetConfig(s.Ctx(), "jira.url")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configDeleteConfigMissingKey() {
	r := s.configRepo()
	s.Require().NoError(r.DeleteConfig(s.Ctx(), "no_such_key"))
}

func (s *testSuite) configGetAllConfigEmpty() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM config")
	s.Require().NoError(err)
	got, err := s.configRepo().GetAllConfig(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]string{}, got)
}

func (s *testSuite) configGetAllConfigAllRows() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM config")
	s.Require().NoError(err)
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "jira.url", "https://example.atlassian.net"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "jira.project", "PROJ"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "export.auto", "true"))

	got, err := r.GetAllConfig(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]string{
		"jira.url":     "https://example.atlassian.net",
		"jira.project": "PROJ",
		"export.auto":  "true",
	}, got)
}

func (s *testSuite) configSetConfigIssuePrefixUnchanged() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "issue_prefix", "bd"))
	v, err := r.GetConfig(s.Ctx(), "issue_prefix")
	s.Require().NoError(err)
	s.Equal("bd", v)
}

func (s *testSuite) configGetCustomTypesMissing() {
	got, err := s.configRepo().GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *testSuite) configGetCustomTypesEmpty() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", ""))
	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *testSuite) configGetCustomTypesCommaSeparated() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", "molecule,gate,convoy"))
	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"molecule", "gate", "convoy"}, got)
}

func (s *testSuite) configGetCustomTypesJSONArray() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", `["gate","convoy"]`))
	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"gate", "convoy"}, got)
}

func (s *testSuite) configGetCustomTypesTrimsAndSkipsEmpty() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", "  alpha , , beta  ,"))
	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"alpha", "beta"}, got)
}

func (s *testSuite) configGetAllowedPrefixesMissing() {
	got, err := s.configRepo().GetAllowedPrefixes(s.Ctx())
	s.Require().NoError(err)
	s.Equal("", got)
}

func (s *testSuite) configGetAllowedPrefixesRawValue() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "allowed_prefixes", "hacker-news, me-py-toolkit, hq-cv"))
	got, err := r.GetAllowedPrefixes(s.Ctx())
	s.Require().NoError(err)
	s.Equal("hacker-news, me-py-toolkit, hq-cv", got)
}

func (s *testSuite) configGetAdaptiveIDConfigDefaults() {
	got, err := s.configRepo().GetAdaptiveIDConfig(s.Ctx())
	s.Require().NoError(err)
	s.Equal(domain.DefaultAdaptiveConfig(), got)
}

func (s *testSuite) configGetAdaptiveIDConfigOverrides() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "max_collision_prob", "0.05"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "min_hash_length", "4"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "max_hash_length", "7"))

	got, err := r.GetAdaptiveIDConfig(s.Ctx())
	s.Require().NoError(err)
	s.InDelta(0.05, got.MaxCollisionProbability, 0.0001)
	s.Equal(4, got.MinLength)
	s.Equal(7, got.MaxLength)
}

func (s *testSuite) configGetCustomStatusesEmpty() {
	got, err := s.configRepo().GetCustomStatuses(s.Ctx())
	s.Require().NoError(err)
	s.Nil(got)
}

func (s *testSuite) configGetCustomStatusesRows() {
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_statuses (name, category) VALUES (?, ?), (?, ?), (?, ?)",
		"review", string(types.CategoryWIP),
		"archived", string(types.CategoryDone),
		"blocked", string(types.CategoryFrozen),
	)
	s.Require().NoError(err)

	got, err := s.configRepo().GetCustomStatuses(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]types.CustomStatus{
		{Name: "archived", Category: types.CategoryDone},
		{Name: "blocked", Category: types.CategoryFrozen},
		{Name: "review", Category: types.CategoryWIP},
	}, got)
}

func (s *testSuite) configGetInfraTypesMissing() {
	got, err := s.configRepo().GetInfraTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]bool{}, got)
}

func (s *testSuite) configGetInfraTypesEmpty() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.infra", ""))
	got, err := r.GetInfraTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]bool{}, got)
}

func (s *testSuite) configGetInfraTypesCommaSeparated() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.infra", "agent,rig,role"))
	got, err := r.GetInfraTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]bool{"agent": true, "rig": true, "role": true}, got)
}

func (s *testSuite) configGetInfraTypesTrimsAndSkipsEmpty() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.infra", "  agent , , rig  ,"))
	got, err := r.GetInfraTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]bool{"agent": true, "rig": true}, got)
}

func (s *testSuite) configGetAdaptiveIDConfigMalformed() {
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "max_collision_prob", "not-a-float"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "min_hash_length", "nope"))
	s.Require().NoError(r.SetConfig(s.Ctx(), "max_hash_length", ""))

	got, err := r.GetAdaptiveIDConfig(s.Ctx())
	s.Require().NoError(err)
	// All three should fall back to defaults: malformed values are silently
	// ignored to match the embedded GetAdaptiveConfigTx behavior.
	s.Equal(domain.DefaultAdaptiveConfig(), got)
}

func (s *testSuite) configListAllStatusNamesBuiltinsOnly() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_statuses")
	s.Require().NoError(err)

	got, err := s.configRepo().ListAllStatusNames(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"open", "in_progress", "blocked", "deferred", "closed", "pinned", "hooked"}, got)
}

func (s *testSuite) configListAllStatusNamesAppendsCustom() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_statuses")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_statuses (name, category) VALUES (?, ?), (?, ?)",
		"review", string(types.CategoryWIP),
		"archived", string(types.CategoryDone),
	)
	s.Require().NoError(err)

	got, err := s.configRepo().ListAllStatusNames(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{
		"open", "in_progress", "blocked", "deferred", "closed", "pinned", "hooked",
		"archived", "review",
	}, got)
}

func (s *testSuite) configUseCaseListAllStatusNames() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_statuses")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_statuses (name, category) VALUES (?, ?)",
		"audit", string(types.CategoryWIP),
	)
	s.Require().NoError(err)

	uc := domain.NewConfigUseCase(NewConfigSQLRepository(s.Runner()))
	got, err := uc.ListAllStatusNames(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{
		"open", "in_progress", "blocked", "deferred", "closed", "pinned", "hooked",
		"audit",
	}, got)
}

func (s *testSuite) configUC() domain.ConfigUseCase {
	return domain.NewConfigUseCase(NewConfigSQLRepository(s.Runner()))
}

func (s *testSuite) localMeta(key string) string {
	v, err := s.configRepo().GetLocalMetadata(s.Ctx(), key)
	s.Require().NoError(err)
	return v
}

func (s *testSuite) resetLocalMetadata() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM local_metadata")
	s.Require().NoError(err)
}

func (s *testSuite) configReconcileVersionEmptyErrors() {
	_, err := s.configUC().ReconcileVersion(s.Ctx(), "")
	s.Require().Error(err)
}

func (s *testSuite) configReconcileVersionFreshInstall() {
	s.resetLocalMetadata()
	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.2.0")
	s.Require().NoError(err)
	s.Equal("", res.Previous)
	s.Equal("1.2.0", res.Current)
	s.True(res.Migrated)
	s.False(res.Downgrade)
	s.Equal("1.2.0", s.localMeta("bd_version"))
	s.Equal("1.2.0", s.localMeta("bd_version_max"))
}

func (s *testSuite) configReconcileVersionAlreadyCurrent() {
	s.resetLocalMetadata()
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.2.0"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version_max", "1.2.0"))

	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.2.0")
	s.Require().NoError(err)
	s.Equal("1.2.0", res.Previous)
	s.Equal("1.2.0", res.Current)
	s.False(res.Migrated)
	s.False(res.Downgrade)
}

func (s *testSuite) configReconcileVersionUpgrade() {
	s.resetLocalMetadata()
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.2.0"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version_max", "1.2.0"))

	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.3.0")
	s.Require().NoError(err)
	s.Equal("1.2.0", res.Previous)
	s.Equal("1.3.0", res.Current)
	s.True(res.Migrated)
	s.False(res.Downgrade)
	s.Equal("1.3.0", s.localMeta("bd_version"))
	s.Equal("1.3.0", s.localMeta("bd_version_max"))
}

func (s *testSuite) configReconcileVersionDowngradeRefused() {
	s.resetLocalMetadata()
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.3.0"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version_max", "1.3.0"))

	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.2.0")
	s.Require().NoError(err)
	s.Equal("1.3.0", res.Previous)
	s.Equal("1.3.0", res.Current)
	s.False(res.Migrated)
	s.True(res.Downgrade)
	s.Equal("1.3.0", s.localMeta("bd_version"))
	s.Equal("1.3.0", s.localMeta("bd_version_max"))
}

func (s *testSuite) configReconcileVersionDowngradeBelowMaxRefused() {
	s.resetLocalMetadata()
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.1.0"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version_max", "1.3.0"))

	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.2.0")
	s.Require().NoError(err)
	s.False(res.Migrated)
	s.True(res.Downgrade)
	s.Equal("1.1.0", s.localMeta("bd_version"))
	s.Equal("1.3.0", s.localMeta("bd_version_max"))
}

func (s *testSuite) configReconcileVersionCatchUpToMax() {
	s.resetLocalMetadata()
	r := s.configRepo()
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version", "1.1.0"))
	s.Require().NoError(r.SetLocalMetadata(s.Ctx(), "bd_version_max", "1.3.0"))

	res, err := s.configUC().ReconcileVersion(s.Ctx(), "1.3.0")
	s.Require().NoError(err)
	s.Equal("1.1.0", res.Previous)
	s.Equal("1.3.0", res.Current)
	s.True(res.Migrated)
	s.False(res.Downgrade)
	s.Equal("1.3.0", s.localMeta("bd_version"))
	s.Equal("1.3.0", s.localMeta("bd_version_max"))
}

func (s *testSuite) configUseCaseGetConfigMissing() {
	v, err := s.configUC().GetConfig(s.Ctx(), "no_such_key")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configUseCaseGetConfigRoundTrip() {
	uc := s.configUC()
	s.Require().NoError(uc.SetConfig(s.Ctx(), "team.sync_branch", "main"))
	v, err := uc.GetConfig(s.Ctx(), "team.sync_branch")
	s.Require().NoError(err)
	s.Equal("main", v)
}

func (s *testSuite) configUseCaseSetConfigOverwrite() {
	uc := s.configUC()
	s.Require().NoError(uc.SetConfig(s.Ctx(), "k", "v1"))
	s.Require().NoError(uc.SetConfig(s.Ctx(), "k", "v2"))
	v, err := uc.GetConfig(s.Ctx(), "k")
	s.Require().NoError(err)
	s.Equal("v2", v)
}

func (s *testSuite) configUseCaseSetConfigIssuePrefixTrim() {
	uc := s.configUC()
	s.Require().NoError(uc.SetConfig(s.Ctx(), "issue_prefix", "bd-"))
	v, err := uc.GetConfig(s.Ctx(), "issue_prefix")
	s.Require().NoError(err)
	s.Equal("bd", v)
}

func (s *testSuite) configUseCaseDeleteConfigRemoves() {
	uc := s.configUC()
	s.Require().NoError(uc.SetConfig(s.Ctx(), "jira.url", "https://example.atlassian.net"))
	s.Require().NoError(uc.DeleteConfig(s.Ctx(), "jira.url"))
	v, err := uc.GetConfig(s.Ctx(), "jira.url")
	s.Require().NoError(err)
	s.Equal("", v)
}

func (s *testSuite) configUseCaseDeleteConfigMissing() {
	s.Require().NoError(s.configUC().DeleteConfig(s.Ctx(), "no_such_key"))
}

func (s *testSuite) configUseCaseGetAllConfigEmpty() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM config")
	s.Require().NoError(err)
	got, err := s.configUC().GetAllConfig(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]string{}, got)
}

func (s *testSuite) configUseCaseGetAllConfigAllRows() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM config")
	s.Require().NoError(err)
	uc := s.configUC()
	s.Require().NoError(uc.SetConfig(s.Ctx(), "jira.url", "https://example.atlassian.net"))
	s.Require().NoError(uc.SetConfig(s.Ctx(), "jira.project", "PROJ"))

	got, err := uc.GetAllConfig(s.Ctx())
	s.Require().NoError(err)
	s.Equal(map[string]string{
		"jira.url":     "https://example.atlassian.net",
		"jira.project": "PROJ",
	}, got)
}

func (s *testSuite) configGetCustomTypesTablePrecedence() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_types")
	s.Require().NoError(err)
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", "from-config"))
	_, err = s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_types (name) VALUES (?), (?)",
		"from-table-a", "from-table-b")
	s.Require().NoError(err)

	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"from-table-a", "from-table-b"}, got)
}

func (s *testSuite) configGetCustomTypesConfigFallback() {
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_types")
	s.Require().NoError(err)
	r := s.configRepo()
	s.Require().NoError(r.SetConfig(s.Ctx(), "types.custom", "fallback-only"))

	got, err := r.GetCustomTypes(s.Ctx())
	s.Require().NoError(err)
	s.Equal([]string{"fallback-only"}, got)
}
