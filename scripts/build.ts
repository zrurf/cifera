import * as Bun from "bun";

const result = await Bun.build({
    entrypoints: ["./src/index.ts"],
    outdir: "./dist",
    target: "browser",
    format: "iife",
    naming: "cifera.runtime.js",
    minify: true,
    sourcemap: "none"
});

if (!result.success) {
    console.error("Build failed:");
    for (const log of result.logs) {
        console.error(log);
    }
    process.exit(1);
}

console.log("Build succeeded:");
for (const output of result.outputs) {
    console.log(`  ${output.path} (${output.size} bytes)`);
}