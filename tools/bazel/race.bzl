"""go_test_race_off: an existing go_test built without the race detector.

The docker lane (--config=docker) sets --@rules_go//go/config:race for every
target, because pr.yml's container jobs run `go test -race`, except the
Contract corpus job, which does not. A go_test_variant.sh sh_test over this
rule's output runs such a package the way that job does, without a second
go_test rule (whose srcs gazelle would not maintain). The go_test keeps its own
`race = "auto"` and so follows the setting this transition gives it.
"""

_RACE = "@rules_go//go/config:race"

# rules_go's go_transition (go_binary/go_test with race = "off", e.g.
# //cmd/bd:bd_for_tests) records the setting it overrides in this private
# flag. Recording it the same way puts the test's deps in the configuration
# bd_for_tests already builds them in, instead of a second non-race copy of
# the whole graph.
_ORIGINAL_RACE = "@rules_go//go/private/rules:original_race"

def _race_off_impl(settings, _attr):
    out = {_RACE: False, _ORIGINAL_RACE: settings[_ORIGINAL_RACE]}
    if settings[_RACE] and not settings[_ORIGINAL_RACE]:
        out[_ORIGINAL_RACE] = json.encode(True)
    return out

_race_off = transition(
    implementation = _race_off_impl,
    inputs = [_RACE, _ORIGINAL_RACE],
    outputs = [_RACE, _ORIGINAL_RACE],
)

def _go_test_race_off_impl(ctx):
    target = ctx.attr.test[0]
    src = target[DefaultInfo].files_to_run.executable
    out = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.symlink(output = out, target_file = src, is_executable = True)
    runfiles = ctx.runfiles(files = [out]).merge(target[DefaultInfo].default_runfiles)
    return [DefaultInfo(files = depset([out]), runfiles = runfiles, executable = out)]

_go_test_race_off = rule(
    implementation = _go_test_race_off_impl,
    doc = "The executable of `test`, built with --@rules_go//go/config:race=false.",
    attrs = {
        "test": attr.label(
            cfg = _race_off,
            executable = True,
            mandatory = True,
        ),
    },
    executable = True,
)

def go_test_race_off(name, test, **kwargs):
    """Declares `name`, the executable of go_test `test` built without race."""
    _go_test_race_off(name = name, test = test, testonly = True, **kwargs)
