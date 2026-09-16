"""Build the complete b10488 server with the bounded EmbeddingGemma allocator.
Inputs are verified local archives; no download, model pruning or compute change.
Requires Zig 0.14.1, CMake 3.31.6, Ninja 1.11.1.4 on Windows x64.
"""
import argparse, hashlib, json, os, pathlib, shutil, struct, subprocess, tarfile, zipfile
p=argparse.ArgumentParser()
for key in ['source-archive','upstream-runtime','zig','cmake','ninja','output']:
    p.add_argument('--'+key,required=True,type=pathlib.Path)
a=p.parse_args()
for key in vars(a): setattr(a,key,getattr(a,key).resolve())
assert hashlib.sha256(a.source_archive.read_bytes()).hexdigest()=='b83890db3d902d4c49d5ee638bade9967011beca56bbd803173689f615c2a9c3'
assert hashlib.sha256(a.upstream_runtime.read_bytes()).hexdigest()=='6c938f6d79aac96cb90fda673aade20cff9b1b6c1e97de04f4d5d60bca107082'
root=a.output
root.mkdir(parents=True,exist_ok=False)
source_root=root/'source';source_root.mkdir()
with tarfile.open(a.source_archive) as ar:
    for m in ar.getmembers():
        target=(source_root/m.name).resolve()
        if source_root not in target.parents or m.issym() or m.islnk():raise ValueError('unsafe source archive')
    ar.extractall(source_root)
source=next(source_root.iterdir())
context=source/'src/llama-context.cpp'
text=context.read_text(encoding='utf-8');before='bool has_logits     = true;'
assert text.count(before)==1
context.write_text(text.replace(before,'bool has_logits     = !(cparams.embeddings && model.arch == LLM_ARCH_GEMMA_EMBEDDING);'),encoding='utf-8')
runtime=root/'runtime';runtime.mkdir()
with zipfile.ZipFile(a.upstream_runtime) as ar:
    for entry in ar.infolist():
        name=pathlib.PurePosixPath(entry.filename).name
        if (name.startswith('ggml') and name.endswith('.dll')) or name=='libomp140.x86_64.dll':
            with ar.open(entry) as src,(runtime/name).open('wb') as dst:shutil.copyfileobj(src,dst,65536)
config=root/'ggml';config.mkdir()
def run(args):
    subprocess.run([str(v) for v in args],check=True,creationflags=subprocess.CREATE_NO_WINDOW)
def exports(path):
 data=path.read_bytes();pe=struct.unpack_from('<I',data,60)[0];sections=struct.unpack_from('<H',data,pe+6)[0];opt=pe+24;size=struct.unpack_from('<H',data,pe+20)[0]
 def offset(rva):
  for i in range(sections):
   at=opt+size+40*i;vs,va,rs,rp=struct.unpack_from('<IIII',data,at+8)
   if va<=rva<va+max(vs,rs):return rp+rva-va
  return rva
 er=struct.unpack_from('<I',data,opt+112)[0];eo=offset(er);count=struct.unpack_from('<I',data,eo+24)[0];nr=struct.unpack_from('<I',data,eo+32)[0]
 out=[]
 for i in range(count):
  at=offset(struct.unpack_from('<I',data,offset(nr)+4*i)[0]);end=data.index(b'\0',at);out.append(data[at:end].decode())
 return out

parts=[]
for name in ['ggml-base','ggml']:
    deffile=config/(name+'.def');deffile.write_text('LIBRARY '+name+'.dll\nEXPORTS\n'+'\n'.join(exports(runtime/(name+'.dll')))+'\n')
    lib=config/('lib'+name+'.dll.a')
    run([a.zig,'dlltool','-m','i386:x86-64','-D',name+'.dll','-d',deffile,'-l',lib])
    parts.append(f'add_library(ggml::{name} SHARED IMPORTED)\nset_target_properties(ggml::{name} PROPERTIES IMPORTED_IMPLIB "{lib.as_posix()}" IMPORTED_LOCATION "{(runtime/(name+".dll")).as_posix()}" INTERFACE_INCLUDE_DIRECTORIES "{(source/"ggml/include").as_posix()}" INTERFACE_COMPILE_DEFINITIONS "GGML_SHARED;GGML_BACKEND_DL")')
parts.append('set_target_properties(ggml::ggml PROPERTIES INTERFACE_LINK_LIBRARIES ggml::ggml-base)')
(config/'ggml-config.cmake').write_text('\n'.join(parts),encoding='utf-8')
for tool in ['ar','ranlib']:(root/(tool+'.cmd')).write_text('@echo off\n"'+str(a.zig)+'" '+tool+' %*\n')
build=root/'build'
run([a.cmake,'-S',source,'-B',build,'-G','Ninja',f'-DCMAKE_MAKE_PROGRAM={a.ninja}',f'-DCMAKE_C_COMPILER={a.zig}','-DCMAKE_C_COMPILER_ARG1=cc',f'-DCMAKE_CXX_COMPILER={a.zig}','-DCMAKE_CXX_COMPILER_ARG1=c++',f'-DCMAKE_AR={root / "ar.cmd"}',f'-DCMAKE_RANLIB={root / "ranlib.cmd"}','-DCMAKE_BUILD_TYPE=Release','-DCMAKE_CXX_FLAGS=-static-libstdc++','-DCMAKE_SHARED_LINKER_FLAGS=','-DLLAMA_USE_SYSTEM_GGML=ON',f'-Dggml_DIR={config}','-DLLAMA_BUILD_TESTS=OFF','-DLLAMA_BUILD_EXAMPLES=OFF','-DLLAMA_BUILD_APP=OFF','-DLLAMA_BUILD_UI=OFF','-DLLAMA_BUILD_SERVER=ON','-DLLAMA_BUILD_TOOLS=ON','-DLLAMA_BUILD_COMMON=ON','-DLLAMA_BUILD_NUMBER=10488','-DLLAMA_BUILD_COMMIT=9d77fa172','-DGGML_BACKEND_DL=ON','-DGGML_CPU=ON','-DLLAMA_OPENSSL=OFF','-DBUILD_SHARED_LIBS=OFF'])
run([a.cmake,'--build',build,'--target','llama-server','--parallel','2'])
shutil.copyfile(build/'bin/llama-server.exe',runtime/'llama-server.exe')
archive=root/'llama-b10488-ownward-windows-x64.zip'
with zipfile.ZipFile(archive,'w',compression=zipfile.ZIP_STORED) as z:
    for f in sorted(runtime.iterdir()):
        entry=zipfile.ZipInfo(f.name,(2026,9,16,0,0,0));z.writestr(entry,f.read_bytes())
(root/'build-identity.json').write_text(json.dumps({'source_sha256':hashlib.sha256(a.source_archive.read_bytes()).hexdigest(),'upstream_runtime_sha256':hashlib.sha256(a.upstream_runtime.read_bytes()).hexdigest(),'runtime_archive_sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),'files':{f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in sorted(runtime.iterdir())}},indent=2)+'\n',encoding='utf-8')
