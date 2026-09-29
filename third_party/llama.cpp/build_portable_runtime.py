#!/usr/bin/env python3
"""Build the pinned CPU runtime for Linux/macOS; never download build inputs.

Native builds need Python, CMake, Ninja and a C++17 compiler. A Windows builder
can supply Zig to cross-build the Linux static artifact, which still requires
native execution before release. The receipt binds inputs and resulting bytes.
"""
import argparse
import hashlib
import json
import os
import pathlib
import platform
import shutil
import subprocess
import tarfile
import zipfile

SOURCE = "b83890db3d902d4c49d5ee638bade9967011beca56bbd803173689f615c2a9c3"
PATCH = "embedding-output-allocation-v1"

def sha(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-archive", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--cmake", default="cmake")
    parser.add_argument("--ninja", default="ninja")
    parser.add_argument("--zig", type=pathlib.Path)
    parser.add_argument("--target", choices=["linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64"])
    parser.add_argument("--jobs", type=int, default=4)
    parser.add_argument("--resume", action="store_true", help="reuse this build's already extracted source and compiler cache")
    args = parser.parse_args()
    native_os = {"Linux": "linux", "Darwin": "darwin"}.get(platform.system())
    native_arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
    target = args.target or f"{native_os}-{native_arch}"
    if target not in ("linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64"):
        raise ValueError("specify a supported target")
    if args.jobs < 1 or args.jobs > 32:
        raise ValueError("jobs must be between 1 and 32")
    if sha(args.source_archive) != SOURCE:
        raise ValueError("source archive differs from pinned llama.cpp b10488")
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=args.resume)
    source_root = root / "source"
    if not args.resume:
        source_root.mkdir()
        with tarfile.open(args.source_archive) as archive:
            for member in archive.getmembers():
                path = (source_root / member.name).resolve()
                if source_root not in path.parents or member.issym() or member.islnk():
                    raise ValueError("unsafe source archive")
            archive.extractall(source_root)
    source = next(source_root.iterdir())
    patch_file = source / "src/llama-context.cpp"
    text = patch_file.read_text(encoding="utf-8")
    before = "bool has_logits     = true;"
    after = "bool has_logits     = !(cparams.embeddings && model.arch == LLM_ARCH_GEMMA_EMBEDDING);"
    if args.resume:
        if text.count(after) != 1:
            raise ValueError("resume source does not contain the pinned patch")
    else:
        if text.count(before) != 1:
            raise ValueError("pinned allocation patch no longer applies")
        patch_file.write_text(text.replace(before, after), encoding="utf-8")
    flags = ["-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF", "-DGGML_NATIVE=OFF",
	         "-DGGML_SSE42=OFF", "-DGGML_AVX=OFF", "-DGGML_AVX2=OFF", "-DGGML_FMA=OFF", "-DGGML_F16C=OFF",
             "-DGGML_OPENMP=OFF", "-DGGML_ACCELERATE=OFF", "-DGGML_BLAS=OFF", "-DGGML_BACKEND_DL=OFF",
             "-DLLAMA_BUILD_TESTS=OFF", "-DLLAMA_BUILD_EXAMPLES=OFF", "-DLLAMA_BUILD_APP=OFF",
             "-DLLAMA_BUILD_UI=OFF", "-DLLAMA_USE_PREBUILT_UI=OFF", "-DLLAMA_BUILD_SERVER=ON", "-DLLAMA_OPENSSL=OFF",
             "-DLLAMA_BUILD_NUMBER=10488", "-DLLAMA_BUILD_COMMIT=9d77fa172"]
    if args.zig:
        if not target.startswith("linux-"):
            raise ValueError("cross compilation is supported only for Linux")
        arch = "x86_64" if target.endswith("amd64") else "aarch64"
        triple = arch + "-linux-musl"
        flags += ["-DCMAKE_SYSTEM_NAME=Linux", f"-DCMAKE_SYSTEM_PROCESSOR={arch}",
                  f"-DCMAKE_C_COMPILER={args.zig.resolve()}", f"-DCMAKE_CXX_COMPILER={args.zig.resolve()}",
                  f"-DCMAKE_C_COMPILER_ARG1=cc -target {triple}", f"-DCMAKE_CXX_COMPILER_ARG1=c++ -target {triple}",
                  f"-DCMAKE_ASM_COMPILER={args.zig.resolve()}", f"-DCMAKE_ASM_COMPILER_ARG1=cc -target {triple}",
                  "-DCMAKE_EXE_LINKER_FLAGS=-static"]
        for tool in ("ar", "ranlib"):
            script = root / (tool + (".cmd" if os.name == "nt" else ".sh"))
            if os.name == "nt":
                script.write_text(f'@echo off\n"{args.zig.resolve()}" {tool} %*\n')
            else:
                import shlex
                script.write_text(f'#!/bin/sh\nexec {shlex.quote(str(args.zig.resolve()))} {tool} "$@"\n')
                script.chmod(0o755)
            flags += [f"-DCMAKE_{tool.upper()}={script}"]
        if os.name == "nt":
            host = root / "host-cxx.cmd"
            host.write_text(f'@echo off\n"{args.zig.resolve()}" c++ %*\n')
            flags += [f"-DHOST_CXX_COMPILER={host}"]
    elif target != f"{native_os}-{native_arch}":
        raise ValueError("use a native builder for this target")
    if target.startswith("darwin-"):
        flags += ["-DCMAKE_OSX_DEPLOYMENT_TARGET=12.0"]
    def run(command):
        with (root / "commands.log").open("a", encoding="utf-8") as log:
            log.write("\n" + repr([str(x) for x in command]) + "\n"); log.flush()
            subprocess.run([str(x) for x in command], check=True, stdout=log, stderr=subprocess.STDOUT,
                           creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
    build = root / "build"
    run([args.cmake, "-S", source, "-B", build, "-G", "Ninja", f"-DCMAKE_MAKE_PROGRAM={args.ninja}", *flags])
    run([args.cmake, "--build", build, "--target", "llama-server", "--parallel", args.jobs])
    runtime = root / "runtime"
    runtime.mkdir(exist_ok=args.resume)
    shutil.copyfile(build / "bin/llama-server", runtime / "llama-server")
    (runtime / "llama-server").chmod(0o755)
    output = root / f"llama-b10488-ownward-{target}.zip"
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as archive:
        info = zipfile.ZipInfo("llama-server", (2026, 9, 16, 0, 0, 0))
        info.external_attr = 0o100755 << 16
        archive.writestr(info, (runtime / "llama-server").read_bytes())
    receipt = {"schema": "ownward.runtime-build/v1", "source_sha256": SOURCE, "patch": PATCH,
               "os": target.split("-")[0], "arch": target.split("-")[1], "entry": "llama-server",
               "runtime_archive_sha256": sha(output), "files": {"llama-server": sha(runtime / "llama-server")}}
    (root / "build-identity.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")

if __name__ == "__main__":
    main()
