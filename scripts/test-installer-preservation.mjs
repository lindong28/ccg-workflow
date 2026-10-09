import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const source = await fs.readFile(new URL('../src/utils/installer.ts', import.meta.url), 'utf8');
const helperStart = source.indexOf('export async function isExternallyManagedWrapper');
const helperEnd = source.indexOf('async function installBinaryFile', helperStart);
const start = source.indexOf('    if (await isExternallyManagedWrapper(destBinary))');
const end = source.indexOf('    // Check if binary exists', start);
assert(helperStart >= 0 && helperEnd > helperStart && start >= 0 && end > start);
const helper = source.slice(helperStart, helperEnd).replace('export ', '').replace('path: string', 'path').replace(': Promise<boolean>', '').replace('(error as NodeJS.ErrnoException)', 'error');
const AsyncFunction = Object.getPrototypeOf(async function(){}).constructor;
const check = new AsyncFunction('fs','ctx','destBinary','Buffer',helper+source.slice(start,end)+'\nreturn "continue-version-check";');
const cases = [
 {name:'script',header:'#!/bin/sh',link:false,blocked:true},
 {name:'link',header:'',link:true,blocked:true},
 {name:'dangling link',header:'',link:true,blocked:true},
 {name:'native binary',header:'\x7fELF',link:false,blocked:false},
 {name:'missing',code:'ENOENT',blocked:false},
 {name:'unreadable',code:'EACCES',error:true},
];
for(const c of cases){
 let reads=0;
 const fake={lstat:async()=>{if(c.code)throw Object.assign(new Error('fixture'),{code:c.code});return {isSymbolicLink:()=>c.link,isFile:()=>true};},readFile:async()=>{reads++;return Buffer.from(c.header);}};
 const ctx={result:{errors:[],success:true}};
 if(c.error){await assert.rejects(()=>check(fake,ctx,'fixture/wrapper',Buffer));}
 else {const result=await check(fake,ctx,'fixture/wrapper',Buffer);assert.equal(ctx.result.success,!c.blocked);assert.equal(result,c.blocked?undefined:'continue-version-check');}
 if(c.link)assert.equal(reads,0);
 console.log('PASS',c.name);
}
console.log('Guard block only; no installer, filesystem mutation, execution, or download fallback.');

// init rendering: exercise the actual guard-to-display join with closed fakes.
const vm = await import('node:vm');
const init = await fs.readFile(new URL('../src/commands/init.ts', import.meta.url), 'utf8');
const summary = init.slice(init.indexOf('    // Install selected MCP tools'), init.indexOf('    // ace-tool', init.indexOf('    // Install selected MCP tools')));
const display = init.slice(init.indexOf('    // Show errors if any'), init.indexOf('    // Show MCP resources if user skipped installation'));
assert(summary.includes('spinner.succeed') && display.includes('showBinaryDownloadWarning'));
async function render(result) {
 const output=[];let warnings=0;
 const color = Object.assign(x=>x,{bold:x=>x});
 const sandbox={result,installDir:'fixture',options:{skipPrompt:true},process:{platform:'linux',env:{}},
  console:{log:(...args)=>output.push(args.join(' '))},
  ansis:new Proxy({}, {get:()=>color}),i18n:{t:x=>x},
  spinner:{succeed:x=>output.push('SUCCESS '+x),warn:x=>output.push('WARNING '+x)},
  join:(...x)=>x.join('/'),
  showBinaryDownloadWarning:()=>{warnings++;output.push('Binary download failed (network issue); Download; Place at fixture/bin/codeagent-wrapper');},
  fs:new Proxy({}, {get:()=>()=>{throw new Error('unexpected filesystem access');}})};
 // No import loader or real process/filesystem/provider is available in this VM.
 await new vm.Script('(async()=>{'+summary+display+'})()').runInNewContext(sandbox,{timeout:1000});
 return {output:output.join('\n'),warnings};
}
let failures=0;
for (const c of cases.filter(x=>x.blocked)) {
 const ctx={result:{success:true,errors:[]}};
 const fake={lstat:async()=>({isSymbolicLink:()=>c.link,isFile:()=>true}),readFile:async()=>Buffer.from(c.header)};
 await check(fake,ctx,'fixture/wrapper',Buffer);
 const seen=await render(ctx.result);
 try {
  assert.equal(ctx.result.binInstalled,undefined);
  assert.equal(ctx.result.success,false);
  assert.equal(seen.warnings,0,'protected entry must not become download failure');
  assert(!seen.output.includes('init --force'),'protected entry must not suggest forced reinstall');
  assert(!seen.output.includes('SUCCESS'),'protected entry must not claim verified installation');
  assert(seen.output.includes('未验证') && seen.output.includes('fixture/wrapper'),'preserved result needs path and unverified scope');
  console.log('PASS guard -> init display',c.name);
 } catch(e) { failures++;console.log('FAIL guard -> init display',c.name,e.message); }
}
const failed = await render({success:false,errors:['fixture download failure']});
assert.equal(failed.warnings,1);console.log('PASS actual binary failure still warns');
const installed = await render({success:true,errors:[],binInstalled:true,binPath:'fixture/bin'});
assert.equal(installed.warnings,0);assert(installed.output.includes('init:installedBinary'));console.log('PASS verified binary still reports installed');
console.log(JSON.stringify({displayCases:5,failed:failures}));
if(failures)process.exitCode=1;
