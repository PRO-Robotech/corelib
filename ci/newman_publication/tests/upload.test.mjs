// CI-NP-03/04. Independent test-only holder; expected results come from accepted interface.
import {test,after,mock} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,writeFileSync,mkdirSync,mkdtempSync,existsSync,copyFileSync,rmSync} from 'node:fs';
import {spawnSync} from 'node:child_process';
import {fileURLToPath,pathToFileURL} from 'node:url';
import path from 'node:path';
import {tmpdir} from 'node:os';
import {digest,CANARY} from './upload/sdk.mjs';
import {fixedMain,inputs} from './upload/main-driver.mjs';

const ROOT=path.resolve(fileURLToPath(new URL('../../../',import.meta.url)));
const HERE=path.dirname(fileURLToPath(import.meta.url));
const EVIDENCE=process.env.CI_NP_UPLOAD_EVIDENCE;
assert.ok(EVIDENCE,'set CI_NP_UPLOAD_EVIDENCE to a private capture directory');mkdirSync(EVIDENCE,{recursive:true});
const TEMP=mkdtempSync(path.join(tmpdir(),'np-upload-holder-'));
const SDK=process.env.CI_NP_SDK_ROOT;
const safeEnv=Object.fromEntries(Object.entries(process.env).filter(([k])=>!k.startsWith('GIT_')&&!/TOKEN|SECRET|PASSWORD|CREDENTIAL|AUTHORIZATION/i.test(k)));
safeEnv.GOWORK='off';safeEnv.PYTHONDONTWRITEBYTECODE='1';safeEnv.CI_NP_EVIDENCE_DIR=EVIDENCE;
const gitHead=spawnSync('git',['rev-parse','HEAD'],{cwd:ROOT,env:safeEnv,encoding:'utf8'});assert.equal(gitHead.status,0);
const rows=[];const declared=[];let fixture,prerequisites,prerequisiteError;
function processCapture(name,cmd,args,options={}) {
  const p=spawnSync(cmd,args,{cwd:ROOT,env:safeEnv,encoding:'utf8',timeout:60000,maxBuffer:5*1024*1024,...options});
  writeFileSync(path.join(EVIDENCE,name+'.stdout'),p.stdout??'');writeFileSync(path.join(EVIDENCE,name+'.stderr'),p.stderr??'');
  writeFileSync(path.join(EVIDENCE,name+'.execution.json'),JSON.stringify({command:[cmd,...args],cwd:ROOT,rc:p.status,signal:p.signal,stdout_sha256:digest(p.stdout??''),stderr_sha256:digest(p.stderr??'')},null,2));
  return p;
}
try {
  assert.match(process.version,/^v24\./);assert.ok(SDK);
  const p=processCapture('fixture','python3',[path.join(HERE,'upload/fixture.py'),path.join(TEMP,'fixture')]);assert.equal(p.status,0,'real fixture/project/check prerequisite');fixture=JSON.parse(p.stdout);
  const fpath=path.join(EVIDENCE,'fixture.json');writeFileSync(fpath,JSON.stringify(fixture,null,2));
  const sdk=processCapture('sdk',process.execPath,[path.join(HERE,'upload/prerequisites.mjs'),fpath,path.join(EVIDENCE,'prerequisites.json')]);assert.equal(sdk.status,0,'real Node24/SDK/OS prerequisite');prerequisites=JSON.parse(readFileSync(path.join(EVIDENCE,'prerequisites.json')));
} catch(e) {prerequisiteError=e;}
let loaded;
async function publisher() {
  if(prerequisiteError)throw new Error('NOT_EXECUTED: prerequisite failed; see captures');
  const file=path.join(ROOT,'ci/newman_publication/action/publish.mjs');
  if(!existsSync(file))throw new Error('CAPABILITY_ABSENT: publishSnapshot after successful prerequisites');
  loaded??=await import(pathToFileURL(file).href);assert.equal(typeof loaded.publishSnapshot,'function');return loaded.publishSnapshot;
}
function delta(a,b,prefix='') {
  if(Object.is(a,b))return [];
  if(a&&b&&typeof a==='object'&&typeof b==='object'&&!Buffer.isBuffer(a)&&!Buffer.isBuffer(b))return [...new Set([...Object.keys(a),...Object.keys(b)])].flatMap(k=>delta(a[k],b[k],prefix?prefix+'.'+k:k));
  return [prefix];
}
function define(id,axis,run) {
  declared.push({id,scenario:id.startsWith('snapshot')?'CI-NP-04':'CI-NP-03',axis});
  test(id,{timeout:20000},async t=>{
    const row={id,axis,prerequisites:!prerequisiteError};rows.push(row);
    try {if(prerequisiteError)throw new Error('NOT_EXECUTED: prerequisite failed; see captures');await run(t,row);row.outcome='PASS';}
    catch(e){row.outcome=String(e.message).startsWith('CAPABILITY_ABSENT')?'CAPABILITY_ABSENT':String(e.message).startsWith('NOT_EXECUTED')?'NOT_EXECUTED':'FAIL';row.signature=String(e.message).split('\n')[0];throw e;}
  });
}
function output(result,status,code,bytes=null) {
  assert.deepEqual(Object.keys(result).sort(),['schema_version','status','code','artifact_id','archive_bytes','archive_sha256'].sort());
  assert.equal(result.schema_version,1);assert.equal(result.status,status);
  if(Array.isArray(code))assert.ok(code.includes(result.code));else assert.equal(result.code,code);
  assert.equal(JSON.stringify(result).includes(CANARY),false);
  assert.ok(Number.isSafeInteger(result.archive_bytes)&&result.archive_bytes>=0);
  if(result.archive_sha256!==null)assert.match(result.archive_sha256,/^[a-f0-9]{64}$/);
  if(status==='PUBLISHED'){assert.equal(result.artifact_id,731);assert.equal(result.archive_bytes,bytes.length);assert.equal(result.archive_sha256,digest(bytes));}
  else assert.equal(result.artifact_id,null);
}
function scene(name) {
  const f=fixture.fixtures[0];const dir=path.join(TEMP,name);mkdirSync(dir);const archivePath=path.join(dir,'publication.zip');copyFileSync(f.archivePath,archivePath);
  const bytes=readFileSync(archivePath);let receives=[],checks=[],abortSeen=false;
  const checker=async ({bytes:checked,manifestPath,signal})=>{
    assert.equal(manifestPath,f.manifestPath);assert.ok(signal instanceof AbortSignal);checks.push(Buffer.from(checked));
    const p=spawnSync('python3',['-m','ci.newman_publication','check','--archive','-','--manifest',manifestPath],{cwd:ROOT,env:safeEnv,input:checked,timeout:10000});
    assert.equal(p.stderr.length,0);assert.ok([0,1,3].includes(p.status));return JSON.parse(p.stdout);
  };
  const transport=async req=>{assert.deepEqual(Object.keys(req).sort(),['name','stream','size','sha256'].sort());assert.equal(req.name,'newman-publication-17-2-0');const chunks=[];for await(const b of req.stream)chunks.push(Buffer.from(b));const got=Buffer.concat(chunks);receives.push(got);assert.equal(req.size,got.length);assert.equal(req.sha256,digest(got));return {artifactId:731,size:got.length,sha256:digest(got)};};
  return {f,dir,bytes,receives,checks,checker,transport,options:{archivePath,manifestPath:f.manifestPath,run:{id:17,attempt:2,shardIndex:0},checker,transport}};
}
async function lawful(pub,s) {const r=await pub(s.options);output(r,'PUBLISHED','COMPLETE',s.bytes);assert.deepEqual(s.receives,[s.bytes]);assert.deepEqual(s.checks,[s.bytes]);s.receives.length=0;s.checks.length=0;return r;}
define('snapshot-lawful-full-bytes','real project/check → receiving bytes',async()=>{const p=await publisher(),s=scene('lawful');await lawful(p,s);});
for(const change of ['replace-path-after-check','mutate-checker-copy'])define('snapshot-'+change,'only mutable input copy changes after checked bytes',async(_t,row)=>{
  const p=await publisher(),s=scene(change);await lawful(p,s);
  if(change==='replace-path-after-check')s.options.afterCheck=()=>writeFileSync(s.options.archivePath,CANARY);
  else s.options.checker=async request=>{const verdict=await s.checker(request);request.bytes.fill(120);return verdict;};
  row.changed=['one '+change];const result=await p(s.options);
  if(result.status==='PUBLISHED'){output(result,'PUBLISHED','COMPLETE',s.bytes);assert.deepEqual(s.receives,[s.bytes]);}
  else {output(result,'NOT_EXECUTED',['INPUT_UNAVAILABLE','CHECKER_INVALID','CHECKER_INCOMPLETE','INTERNAL_ERROR']);assert.equal(s.receives.length,0);}

});
for(const kind of ['add','remove','change'])define('snapshot-before-'+kind,'one ZIP member delta',async(_t,row)=>{
  const p=await publisher(),s=scene('before-'+kind);await lawful(p,s);const mutant=fixture.mutations.find(x=>x.kind===kind);assert.equal(mutant.changed_members.length,1);row.changed=mutant.changed_members;
  copyFileSync(mutant.path,s.options.archivePath);const r=await p(s.options);output(r,'NOT_EXECUTED','CHECKER_INCOMPLETE');assert.equal(s.receives.length,0);
});
for(const [name,value,code]of [['missing-file',null,'INPUT_UNAVAILABLE'],['raw-report','raw','CHECKER_INCOMPLETE']])define('input-'+name,'archive input only',async(_t,row)=>{
  const p=await publisher(),s=scene(name);await lawful(p,s);if(value===null)rmSync(s.options.archivePath);else copyFileSync(s.f.rawPath,s.options.archivePath);row.changed=['archive bytes/existence'];output(await p(s.options),'NOT_EXECUTED',code);assert.equal(s.receives.length,0);
});
const fields=[['operation','scan'],['archive_sha256','0'.repeat(64)],['archive_bytes',0],['schema_version',2],['files_declared',true],['findings',true],['extra',CANARY]];
for(const [field,value]of fields)define('checker-invalid-'+field,'one checker response field',async(_t,row)=>{
  const p=await publisher(),s=scene('invalid-'+field);await lawful(p,s);s.options.checker=async request=>{const good=await s.checker(request),bad={...good,[field]:value};row.changed=delta(good,bad);assert.deepEqual(row.changed,[field]);return bad;};output(await p(s.options),'NOT_EXECUTED','CHECKER_INVALID');assert.equal(s.receives.length,0);
});
for(const field of ['files_declared','files_checked'])define('checker-incomplete-'+field,'one census field',async(_t,row)=>{
  const p=await publisher(),s=scene('incomplete-'+field);await lawful(p,s);s.options.checker=async req=>{const good=await s.checker(req),bad={...good,[field]:0};row.changed=delta(good,bad);assert.deepEqual(row.changed,[field]);return bad;};output(await p(s.options),'NOT_EXECUTED',['CHECKER_INVALID','CHECKER_INCOMPLETE']);assert.equal(s.receives.length,0);
});
for(const mode of ['missing','throws','nonclean','finding'])define('checker-'+mode,'one checker boundary outcome',async(_t,row)=>{
  const p=await publisher(),s=scene('checker-'+mode);await lawful(p,s);row.changed=['checker boundary outcome'];
  if(mode==='missing')s.options.checker=undefined;
  else if(mode==='throws')s.options.checker=async()=>{throw new Error(CANARY);};
  else s.options.checker=async req=>({...await s.checker(req),status:mode==='finding'?'FINDING':'NOT_EXECUTED',code:mode==='finding'?'SECRET_MATERIAL':'INTERRUPTED',findings:mode==='finding'?1:0});
  const r=await p(s.options);output(r,mode==='finding'?'FINDING':'NOT_EXECUTED',mode==='finding'?'SECRET_MATERIAL':mode==='nonclean'?'CHECKER_INCOMPLETE':'CHECKER_UNAVAILABLE');assert.equal(s.receives.length,0);
});
for(const [field,value]of [['maxArchiveBytes',0],['maxArchiveBytes',true],['maxArchiveBytes',1.5],['maxArchiveBytes',67108865],['checkerTimeoutMs',0],['checkerTimeoutMs',true],['checkerTimeoutMs',1.5],['checkerTimeoutMs',120001]])define('budget-invalid-'+field+'-'+String(value),'one budget value',async(_t,row)=>{
  const p=await publisher(),s=scene('budget-'+field+'-'+value);await lawful(p,s);s.options[field]=value;row.changed=[field];output(await p(s.options),'NOT_EXECUTED','INVALID_REQUEST');assert.equal(s.receives.length,0);
});
define('budget-exact-and-one-below','maxArchiveBytes one-byte boundary',async(_t,row)=>{const p=await publisher(),s=scene('budget-boundary');s.options.maxArchiveBytes=s.bytes.length;await lawful(p,s);const good={maxArchiveBytes:s.bytes.length},bad={maxArchiveBytes:s.bytes.length-1};row.changed=delta(good,bad);assert.deepEqual(row.changed,['maxArchiveBytes']);s.options.maxArchiveBytes=bad.maxArchiveBytes;output(await p(s.options),'NOT_EXECUTED','LIMIT_EXCEEDED');assert.equal(s.receives.length,0);});
define('checker-timeout-aborts','controlled timer + abort observation',async(t,row)=>{
  const p=await publisher(),s=scene('timeout');await lawful(p,s);let started;const entered=new Promise(r=>started=r);let aborted=false;
  s.options.checkerTimeoutMs=17;s.options.checker=({signal})=>new Promise(()=>{signal.addEventListener('abort',()=>{aborted=true;},{once:true});started();});
  t.mock.timers.enable({apis:['setTimeout']});const running=p(s.options);await entered;t.mock.timers.tick(16);assert.equal(aborted,false);t.mock.timers.tick(1);const r=await running;t.mock.timers.reset();output(r,'NOT_EXECUTED','CHECKER_TIMEOUT');assert.equal(aborted,true);assert.equal(s.receives.length,0);row.changed=['checker completion'];
});
for(const mode of ['throws','digest','size','id'])define('transport-'+mode,'one transport outcome',async(_t,row)=>{const p=await publisher(),s=scene('transport-'+mode);await lawful(p,s);s.options.transport=async req=>{if(mode==='throws')throw new Error(CANARY);const good=await s.transport(req);const field={digest:'sha256',size:'size',id:'artifactId'}[mode];const bad={...good,[field]:mode==='digest'?'0'.repeat(64):0};row.changed=delta(good,bad);assert.deepEqual(row.changed,[field]);return bad;};output(await p(s.options),'NOT_EXECUTED',mode==='throws'?'TRANSPORT_UNAVAILABLE':'TRANSPORT_MISMATCH');});

const mainCases=[['lawful',{},false],['stall-checker',{'checker-timeout-ms':'500'},false],['malformed-checker',{},false],['blob-error',{},false],['create-not-ok',{},false],['finalize-not-ok',{},false],['bad-artifact-id',{},false],['missing-runtime',{},true],['bad-integer',{'run-id':'17junk'},false],['bad-budget',{'max-archive-bytes':'67108865'},false]];
for(const [mode,override,withoutToken]of mainCases)define('fixed-main-'+mode,'fixed Node24 action and genuine SDK',async(_t,row)=>{
  const main=path.join(ROOT,'ci/newman_publication/action/main.mjs'),action=path.join(ROOT,'ci/newman_publication/action/action.yml');
  if(!existsSync(main)||!existsSync(action))throw new Error('CAPABILITY_ABSENT: fixed main/action after successful prerequisites');
  const base=path.join(TEMP,'main-'+mode);mkdirSync(base);const r=fixedMain({root:ROOT,base,sdkHome:SDK,fixture:fixture.fixtures[0],mode,override,withoutToken});
  assert.equal(r.process.error,undefined,'entrypoint process must finish within harness bound');assert.equal(r.process.stderr,'');assert.equal(r.process.stdout.includes(CANARY),false);const answer=JSON.parse(r.process.stdout);
  assert.equal(r.rows.filter(x=>x.kind==='unexpected-network'||x.kind==='unexpected-child').length,0);
  const children=r.rows.filter(x=>x.kind==='child');for(const child of children){assert.equal(child.canonical,true);assert.equal(child.credentialValues,0);assert.equal(child.hasSignal,true);}
  assert.ok(r.rows.filter(x=>x.kind==='process-exit').every(x=>x.liveChildren.length===0),'producer must terminate own child before exit');
  if(mode==='lawful'){output(answer,'PUBLISHED','COMPLETE',readFileSync(fixture.fixtures[0].archivePath));assert.equal(r.process.status,0);assert.deepEqual(r.received,readFileSync(fixture.fixtures[0].archivePath));assert.equal(children.length,1);assert.deepEqual(r.rows.filter(x=>x.kind==='request').map(x=>x.method),['CreateArtifact','FinalizeArtifact']);}
  else {assert.equal(r.process.status,3);output(answer,'NOT_EXECUTED',['INVALID_REQUEST','CHECKER_TIMEOUT','CHECKER_INVALID','CHECKER_INCOMPLETE','TRANSPORT_UNAVAILABLE','TRANSPORT_MISMATCH']);if(['stall-checker','malformed-checker','bad-integer','bad-budget','missing-runtime'].includes(mode))assert.equal(r.rows.filter(x=>x.kind==='blob').length,0);}
  if(mode==='stall-checker'){assert.equal(answer.code,'CHECKER_TIMEOUT');assert.equal(children.length,1);const closed=r.rows.filter(x=>x.kind==='child-closed');assert.equal(closed.length,1);assert.ok(closed[0].signal||closed[0].code!==null);}
  row.boundary=r.rows;row.changed=mode==='lawful'?[]:[mode];
  cpEvidence(base,path.join(EVIDENCE,'main-'+mode));
});
function cpEvidence(source,target){mkdirSync(target);for(const name of ['stdout','stderr','boundary.jsonl','boundary.jsonl.bytes'])if(existsSync(path.join(source,name)))copyFileSync(path.join(source,name),path.join(target,name));}
writeFileSync(path.join(EVIDENCE,'declared.json'),JSON.stringify(declared,null,2));
after(()=>{mock.timers.reset();const summary={source_root:ROOT,source_commit:gitHead.stdout.trim(),node:process.version,prerequisites:prerequisites??null,prerequisite_error:prerequisiteError?String(prerequisiteError.message):null,declared:declared.length,executed:rows.length,outcomes:Object.fromEntries(['PASS','FAIL','CAPABILITY_ABSENT','NOT_EXECUTED'].map(k=>[k,rows.filter(x=>x.outcome===k).length])),rows};writeFileSync(path.join(EVIDENCE,'summary.json'),JSON.stringify(summary,null,2));rmSync(TEMP,{recursive:true,force:true});assert.equal(rows.length,declared.length);assert.ok(rows.length>0);});
