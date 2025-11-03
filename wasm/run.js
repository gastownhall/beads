#!/usr/bin/env node
// Node.js wrapper for bd.wasm

const fs = require('fs');
const path = require('path');

// Load wasm_exec.js from Go distribution
require('./wasm_exec.js');

// Load the WASM binary
const wasmPath = path.join(__dirname, 'bd.wasm');
const wasmBuffer = fs.readFileSync(wasmPath);

// Create Go runtime instance
const go = new Go();

// Pass command-line arguments to Go
// process.argv[0] is 'node', process.argv[1] is this script
// So we want process.argv.slice(1) to simulate: bd <args>
go.argv = ['bd'].concat(process.argv.slice(2));

// Instantiate and run the WASM module
WebAssembly.instantiate(wasmBuffer, go.importObject).then((result) => {
    go.run(result.instance);
}).catch((err) => {
    console.error('Failed to run WASM:', err);
    process.exit(1);
});
