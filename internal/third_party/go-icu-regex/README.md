# ICU Regular Expressions in Go

Minimal bindings to [ICU4C](https://github.com/unicode-org/icu)'s regex implementation, for use in Go.

This package is not intended to be a general purpose binding. It's primary purpose is to support [dolt](https://github.com/dolthub/dolt)'s need for ICU-compatible regexes in order to implement MySQL-compatible functionality.

# Use

```go
// Create a regex
regex := regex.CreateRegex(1024)
defer regex.Close()
// Set its pattern
regex.SetRegexString(context.TODO(), "[abc]+", regex.RegexFlags_None)
// Set its match string
regex.SetMatchString(context.TODO(), "123abcabcabcdef")
// Extract a matching substring; note start and occurence number are 1 indexed.
substr, ok, err := regex.Substring(context.TODO(), 1, 1)
assert.NoError(t, err)
assert.True(t, ok)
assert.Equals(t, substr, "abcabcabc")
```

# Building and Dependencies

This library, and consequently anything that depends on it, requires ICU4C to build and link against. This library does not ship a pre-compiled version of ICU4C and does not build ICU4C alongside itself as part of its Cgo binding. Consequently, building this library or anything that depends on it requires a C++ toolchain and a version of ICU4C installed.

For Windows, this library currently only supports MinGW. We are happy to accept changes to support other toolchains based on Go build tags, for example.

For Linux, a package like `libicu-dev` typically has the necessary library.

For Windows, with msys2, `pacman -S icu-devel` installs the necessary development libraries.

For macOS, `brew install icu4c` will install the necsesary library, but it is not on the default search path of the toolchain. Building with something like `CGO_CPPFLAGS=-I$(brew --cellar icu4c)/$(ls $(brew --cellar icu4c))/include CGO_LDFLAGS=-L$(brew --cellar icu4c)/$(ls $(brew --cellar icu4c))/lib` is potentially necessary.

There is some support for statically linking ICU4C, by building with the build tag `icu_static`. Currently this only changes the linker line for a Windows build. For a macOS or Linux build to link it statically, the build tag `icu_static` should still be used, but it should also be the case that the dynamic libraries are not installed.

When using a self-built dynamic library on macOS, the resulting binaries work best if `runConfigureICU` is run with `--enable-rpath`, so that the ICU4C dynamic libraries are discoverable by the built binary at their installed location.
