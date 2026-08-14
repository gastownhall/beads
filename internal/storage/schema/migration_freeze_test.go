package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"
)

// Migration freeze (Beads 1.2 release remediation).
//
// Migrations 0001–0065 (and ignored/ 0001–0024) are SHIPPED: the 1.2.x
// releases published schema v65 to thousands of installs, so these files are
// now the de-facto on-disk contract of every migrated database. Amending a
// shipped migration forks installed schemas from freshly-migrated ones — the
// exact skew the smart gate's fork-skew detection exists to catch at runtime.
// This test catches it in CI instead: the content hash of every shipped
// up-migration is pinned; any change must land as a NEW migration (0066+ /
// ignored 0025+) and extend this table in the same commit that ships it.
//
// If this test fails because you edited a shipped migration: revert the edit
// and write a new migration. There is no legitimate reason to update a pinned
// hash for an already-released version.

const (
	frozenMainThrough    = 65
	frozenIgnoredThrough = 24
)

var frozenMigrationHashes = map[string]string{
	"0001_create_issues.up.sql":                            "0c9bc4d97e2c5ee5d276c952333b934cd7d7d18210997d6bfd72d17f0fdb14f0",
	"0002_create_dependencies.up.sql":                      "9ca011fa0b24ddf26f689eebf347cc2f80dc90256b099d91b993ec82c4d74da5",
	"0003_create_labels.up.sql":                            "1780f0dc941d97b74af53d5603646d05e83d94eee5a02ac68ccf929200deaec0",
	"0004_create_comments.up.sql":                          "35ac76b3d0b2d6287cad1329c77c8a0562bb6310ba0ac34ae273d72d4bed0e2a",
	"0005_create_events.up.sql":                            "c8cbe4bebbe0f92c8a0ccc2a1a0d11ad442635811c2dab093c34c9341a8499b2",
	"0006_create_config.up.sql":                            "435f5ef129dd4999a9b393c98d633038f0e443280a26e63f7d4315bf025dab1f",
	"0007_create_metadata.up.sql":                          "f92c4d3dd32cae489142de34c4706900736c91cdc53b0d54e87e71656b828dc9",
	"0008_create_child_counters.up.sql":                    "99db9b4b5a785ddd209dff628364c4c1eff549c3ca5ec48a64c604b15ef95bfb",
	"0009_create_issue_snapshots.up.sql":                   "269ef83f083766b7f2fd2dfd2814ef36a585ae4aa18bcab0468a713d48595e48",
	"0010_create_compaction_snapshots.up.sql":              "cb34cbe311fd89a17d72067f449a65089f2fc9a7c4b3774d54448db5ebc3d659",
	"0011_create_repo_mtimes.up.sql":                       "bde5204466541dddb53a652fe58561c08b6b3c1fa7feb38a0a582d432d76bda6",
	"0012_create_routes.up.sql":                            "68e8ef1fc336e8ca6973f6b927b09d3206a7ebd96db9c4cdceab52fb8498c431",
	"0013_create_issue_counter.up.sql":                     "7ddb30dabe32a66197de78f62400f41d000cf43d0ba8ba2a3943536504dddcdb",
	"0014_create_interactions.up.sql":                      "0e17f1cb9fc5ee5a7c43ca8e02b9fd59814f8fd4372f9ffb17b50c34388614ce",
	"0015_create_federation_peers.up.sql":                  "6aa04b3f66566b243c9a4fc09136af5c17f0ffe947a489245e3c451b995a59b9",
	"0016_default_config.up.sql":                           "5d556c01404eb06acfb9df2b20fde36a2e91bd8a7d14462acbd3c0ae928ea286",
	"0017_create_ready_issues_view.up.sql":                 "4bab7acbe2d8e441f156cc5912f421dcb214b9b08d7958eacc8e3a7318ab5f57",
	"0018_create_blocked_issues_view.up.sql":               "84dc6a3950a5447e6665e3d98b43f11a7631dc4e53bab1be332d135e51c9511f",
	"0019_wisps_dolt_ignore.up.sql":                        "3df660feb6b64c84ef74c408fe8f5840a2815936534362ed123122f795a21c26",
	"0020_create_wisps.up.sql":                             "bca95662441635454b879bb44ca1e607df9c5efd8fd802be3d53a7371a5ced86",
	"0021_create_wisp_auxiliary.up.sql":                    "d19284a2be70a448db7746ae009cb264dc86dac0d9094aae9fe80a713a08ee70",
	"0022_wisp_dep_type_index.up.sql":                      "fc1375e64620cd4264516718d8f924d88131881f4c00c51471b7630c11b74a45",
	"0023_add_no_history_column.up.sql":                    "d11cd7cb462239e49c0b2f9b93ac73771106d5450ebc3422bb7737748c6f967b",
	"0024_create_custom_status_type_tables.up.sql":         "816da3feeed531ca9787dca8d4b8072dd94b64d573cf4b6751c61f2390c88a68",
	"0025_update_ready_issues_view.up.sql":                 "09782d195f22d620ba41802856f9eb6e1a10f68bc5bcc8f3616624f8c28b576a",
	"0026_update_blocked_issues_view.up.sql":               "33daef6b5a07364fc5f59ab59b0f5c677dcf98d9bd0866c7c3bea874f1f01348",
	"0027_add_started_at.up.sql":                           "dd3b58a8bbed4c49e855a6002981a285f10b6094938534dd3da9b121155f24d6",
	"0028_local_state_dolt_ignore.up.sql":                  "24a816406a6f40c66db98697869d172b2b4366b107788fdd00f44b99abee45c3",
	"0029_create_local_metadata.up.sql":                    "b989fc49ab03e06ff6520f150d6958efdbd3f444b69b824f7b4bb283ca6f2b65",
	"0030_migrate_local_metadata_keys.up.sql":              "8d55135274c1c75e722e802b250911cc574d52a4ba74fbb13edde84f30f9b3a4",
	"0031_wisp_events_created_at_index.up.sql":             "2d5cf3a6d4245b48c1b893efcc17b9fa25f1ea807e61290cf54064451fc58d93",
	"0032_drop_schema_migrations_applied_at.up.sql":        "42b86d6b7352f7e158532cc67f4afa142dc2503ce9f780733c06fc9da3478751",
	"0033_add_wisp_type_column.up.sql":                     "9256746bb0d6ad9ed9afba0f0501259f0a3599b7d7da507dd7d808a13c688c75",
	"0034_add_spec_id_column.up.sql":                       "7add95ead971c57618cfbc52e5cb6771d872785bfc035bde12cd27a76358848e",
	"0035_migrate_infra_to_wisps.up.sql":                   "f917fe319540155572f5c51618cbbe20ec8b9b669d5926d68e3a62629c317c5a",
	"0036_cleanup_autopush_metadata.up.sql":                "e55f1870c2bc031c574b495ebca650365a9c8088d7a8968fb86a19cab81267e8",
	"0037_uuid_primary_keys.up.sql":                        "7526beadee13aa7d6ee2175e9a543210b07c1a4426c382f251ebd31eebe32745",
	"0038_drop_hop_columns.up.sql":                         "002dc637d45840487c35a01ce15cee04872baa45c21f8e20170a4339135a38e7",
	"0039_drop_child_counters_fk.up.sql":                   "db65f241d1e82da41f34b960abc912f78b72e7dc1cb6dd2298974308edf8b768",
	"0040_ignored_tables_also_nonlocal_tables.up.sql":      "c7068d67ad3e2b4852549e04d97f25814121de83db919e220bdcb9bdd9b61e5e",
	"0041_split_dependencies_target.up.sql":                "e84ef393576df31550570fd3217579b1880850c0645b7d2b6ad8cf55a18ac1c0",
	"0042_add_on_update_cascade.up.sql":                    "9d0fbb3d02955d25442c4a628ac518236068a97ecf164edbc814216bd5a55adc",
	"0043_drop_dependencies_generated_column.up.sql":       "548b8a69ab82179b7ee7ab3cc87ef00b3ae28d5a3a4976f42364f6939e384668",
	"0044_update_views_drop_depends_on_id.up.sql":          "531524854cc0e2361817d1ad94ba9470b0e81e378a5422ffb8bce71f3500adf9",
	"0045_update_blocked_view_drop_depends_on_id.up.sql":   "395dcbf186deedb592d8ed144e0b7953455efa0ccf02076d3b63e5e5d05dc241",
	"0046_add_is_blocked.up.sql":                           "4d6a5a5fc5e386f2c538b4f3574b82fb2ba025176997953a0a4fbd2a91cddf68",
	"0047_recompute_mixed_is_blocked.up.sql":               "4aff5a7cc2e1e0250ce4cdb911d010c936681871fb627bceee4b2dc4647dd0a1",
	"0048_widen_event_value_columns.up.sql":                "093c7a41145508879844d16cbe5acfa69baddbb19bf3e05f00bc08720adea657",
	"0049_longtext_large_content_columns.up.sql":           "3359343efb7c28e5803de07e9088670489615d151843d7f23705f3ca8dd6a1e8",
	"0050_dependencies_deterministic_id.up.sql":            "96265ff898753f3bb34549fafa18bbaeb67519705b8cf4e32e67f1058df6841a",
	"0051_drop_aux_id_defaults.up.sql":                     "deb741d5ac5fc1e0cd2e60eb47e84f384ed3455b18d3e2f12cb4e935fe461059",
	"0052_add_date_indexes.up.sql":                         "60ae32bc26c41da2e743c1172f73930d18482b8a98aea08b9de85f6009f9c01d",
	"0053_repair_rig_wisps.up.sql":                         "f13909f0db89f48db694a782d1805eee7ee1b2b2e4dcb3d4b12352333cbcb330",
	"0054_add_lease_columns.up.sql":                        "2e51058ba15be9d87415949bb03c2c7c2f0e0743c5ae0d3e819b617d95f61680",
	"0055_move_leases_to_table.up.sql":                     "2237ddbe13f5a783c65da3bb1292d9a5d89a5f22fddb0c33d2db57f1492df22c",
	"0056_add_comments_keyset_index.up.sql":                "acc28e9b1aa7272c16c86fcb9fd4a2038ae47729a60b53bb75f3a9df05cd3a82",
	"0057_events_value_columns_idempotent_longtext.up.sql": "f86e0f77be2e58affe1c485767b8dd450408d782c0413a41948ca0c0b6d72d06",
	"0058_heal_wisp_dependencies_split_constraints.up.sql": "9c1c6d557a03e71cf6b49123831c9f5b5f910fcea3ca51e982ec4acce04ea6ec",
	"0059_recompute_null_gate_is_blocked.up.sql":           "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394",
	"0060_add_storage_class.up.sql":                        "40419632ba75d92dfcf08e16e77a780f014172a17068f6021937df9086a24588",
	"0061_content_derived_aux_ids.up.sql":                  "d2d65b92789f137da8ee586067c862a9c7744921ccac421e1b793f42050b62a2",
	"0062_events_dolt_ignore.up.sql":                       "c7de6089997344784a71e7d82ea5e417baaf66320b4533d225a4a6bc3c74222a",
	"0063_create_provenance_events.up.sql":                 "1ef173be07ca58d17cb09ba3b3f5ca0c96edfcace8d52ae3adde2fd27e8248bd",
	"0064_create_events_journal.up.sql":                    "25ea9ad8e849dc7e32e7c5850631bedc63a5c5da3bdc58ccb2762fd7a6c758fc",
	"0065_widen_wisp_comments_text.up.sql":                 "c7341bb977daff92e073db2579e63058cef88d43f1e09b13fa153a772caac2a6",
}

var frozenIgnoredMigrationHashes = map[string]string{
	"0001_create_local_state_tables.up.sql":               "a73e9a486124f89e90d50f4597fd1d7ad08e5d0d654459ec87b7520b47f1bb5f",
	"0002_create_wisp_child_counters.up.sql":              "c575c6710169f2e697f3aca60f5f698acc8a64af0edb1e2305b9d377fd845f45",
	"0003_split_wisp_dependencies_target.up.sql":          "f82947deeafe589598e2a1802eddf17712bd322692f924bb5653b0fbd5c46a0d",
	"0004_add_wisp_aux_fks.up.sql":                        "12e18a8bdd214d6f96625e78998433ccc9c2d0ef4d1a35307f2653e725c800b9",
	"0005_drop_wisp_dependencies_generated_column.up.sql": "08780b28ddef6c15f5de558ea08d821b52610f810555739ee1d737779986c607",
	"0006_add_wisp_is_blocked.up.sql":                     "4746f10d7040d0b7a9e127437356de87765d12d2d27ee37c9800191e6b619d14",
	"0007_recompute_wisp_is_blocked.up.sql":               "c3c8d92846a54183cc99afc09f779f6f5ba8030c16ce46b3ee750314bf134627",
	"0008_widen_wisp_event_value_columns.up.sql":          "51375854ec081d2e0b1434b5ff8b6d87092b891636214ebb9f1dbef22438cc1a",
	"0009_aux_row_id_rekey_marker.up.sql":                 "3d1ee4f9dde3342d110474c8090a30c474edafe2290e4372a3fc4607c1fdc030",
	"0010_drop_wisp_id_defaults.up.sql":                   "59d6644667b7e34f9d23261cb694d461ddd4953969e75519d370b0cce8afd0e8",
	"0011_cleanup_orphaned_child_counters.up.sql":         "0efade0ecb620420ee42127d3f67230da108e31f2ff3c4281855a8dc9e187ecd",
	"0012_create_leases.up.sql":                           "e128a1301a2bcb967f684b09318aa5cf7ed9adb5f6dd7d412de3d67d1de2b28d",
	"0013_add_wisp_row_lock.up.sql":                       "127a4af7012769e5f016b13b198b7a57ff8386a10f8a68ffb9ff204989564d1b",
	"0014_add_wisp_comments_keyset_index.up.sql":          "29ad3654218700481b9541331c714ecbbe96dc81c04dc83fba6bf8d35bed7fa4",
	"0015_recompute_null_gate_wisp_is_blocked.up.sql":     "e761d5c3ceb9610edbb2e1d153e82d235cc7d7f0b87f28a45bec43835e6a4e19",
	"0016_add_lease_granted_node.up.sql":                  "31a55421b78d8c90db6cfba5bf8e99e87d6123271575282f761aa077e5d36386",
	"0017_add_wisps_defer_until_index.up.sql":             "1f031aaf2037280e62cbf604748a4157bf25e3a60b3392af7d74835c5059bdd8",
	"0018_aux_row_id_rekey2_marker.up.sql":                "3c2120bf55d15e71a9f96972007c3d914154d17ea37a2b51b44412e3e3df6ef1",
	"0019_create_events.up.sql":                           "db072d67a403e60d5bcbe924b84e473adb09f2326d36ede82fcdab17bb1e2a87",
	"0020_add_wisp_storage_class.up.sql":                  "9ff476e6c73a3a477aec95143d5cbf180517a9484f647447392576f13181a015",
	"0021_widen_wisp_large_content_columns.up.sql":        "3cfa1e07a3d02eb7f791814d9642a93b9af3bf63728943afa05dcb0e489919c8",
	"0022_create_events_journal.up.sql":                   "3d0adcb9f5833b4ba70fb48f88e1043250796feb7173abe000ea4de85eb8b3a5",
	"0023_repair_events_journal_shape.up.sql":             "d0edd83ca4cfbc46bd17799c6497a70c8c011d130d08a00c71daf792ee1dcd51",
	"0024_widen_wisp_comments_text.up.sql":                "69a96330e00643dfeddc4bf6ae9bf3b743a3cfddd86f16dc7b8912758b1b570d",
}

func verifyFrozenMigrations(t *testing.T, fsys fs.FS, dir string, frozen map[string]string, frozenThrough int) {
	t.Helper()
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", dir, err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		version, err := parseVersion(name)
		if err != nil {
			t.Fatalf("unparseable migration filename %q: %v", name, err)
		}
		if version > frozenThrough {
			continue // not yet released — frozen when a release ships it
		}
		want, ok := frozen[name]
		if !ok {
			t.Errorf("shipped migration %s (v%d <= frozen floor %d) has no pinned hash — a released version was renamed or replaced; restore the original file", name, version, frozenThrough)
			continue
		}
		seen[name] = true
		content, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			t.Fatalf("reading embedded %s/%s: %v", dir, name, err)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("shipped migration %s was AMENDED (hash %s, pinned %s) — shipped migrations are frozen; land the change as a new migration instead", name, got, want)
		}
	}
	for name := range frozen {
		if !seen[name] {
			t.Errorf("pinned migration %s is missing from the embedded FS — a released version was deleted or renamed", name)
		}
	}
}

// TestShippedMigrationsAreFrozen pins the content of every up-migration that a
// release has shipped. See the file comment for the policy.
func TestShippedMigrationsAreFrozen(t *testing.T) {
	verifyFrozenMigrations(t, upMigrations, "migrations", frozenMigrationHashes, frozenMainThrough)
}

// TestShippedIgnoredMigrationsAreFrozen is the ignored-sequence twin. The
// ignored tables are clone-local, so an amendment does not fork synced
// history — but it DOES make freshly-materialized clones diverge from
// long-lived ones, the same class of bug one plane over.
func TestShippedIgnoredMigrationsAreFrozen(t *testing.T) {
	verifyFrozenMigrations(t, upIgnoredMigrations, "migrations/ignored", frozenIgnoredMigrationHashes, frozenIgnoredThrough)
}
