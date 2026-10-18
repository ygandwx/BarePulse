import assert from 'node:assert/strict';
import {mount} from '../../frontend/console.js';

// A deliberately small DOM surface for the actual console module. Requests are
// controlled promises; no browser, HTTP server or device access is involved.
class Element {
  constructor(tag) {
    this.tagName=tag;
    this.children=[];
    this.textContent='';
    this.value='';
    this.disabled=false;
    this.className='';
  }
  append(...items) {
    for(const item of items) {
      this.children.push(item);
      if(this.tagName==='select'&&!this.value)this.value=item.value;
    }
  }
  setAttribute() {}
  replaceChildren(...items) {this.children=items;}
  remove() {}
}
globalThis.document={createElement:tag=>new Element(tag)};

const ready=(name,allowed=true)=>({mode:'simulated',message:`${name} ready`,request_allowed:allowed,queue_expected:false});
async function fixture() {
  const pending=[];
  let controlled=false;
  const api=async path=>{
    if(path==='/api/v1/servers')return [{id:'A',name:'Server A'},{id:'B',name:'Server B'}];
    if(path==='/api/v1/console-sessions')return [];
    if(path.endsWith('/console-readiness')) {
      if(!controlled)return ready('A');
      return await new Promise((resolve,reject)=>pending.push({path,resolve,reject}));
    }
    throw new Error(`Unexpected API call: ${path}`);
  };
  const root=new Element('main');
  const dispose=await mount(root,api,{role:'customer'});
  const form=root.children.find(e=>e.tagName==='form');
  assert.ok(form,'actual module must render the request form');
  const [select,request,status]=form.children;
  const error=root.children[root.children.indexOf(form)+1];
  controlled=true;
  return {
    dispose,pending,select,request,status,error,
    selectServer(id) {select.value=id;return select.onchange();},
    snapshot() {return {selected:select.value,disabled:request.disabled,status:status.textContent,error:error.textContent};},
  };
}

let scenarios=0;
async function test(name,fn) {
  await fn();
  scenarios++;
  console.log(`PASS ${name}`);
}

await test('older success cannot replace the latest readiness',async()=>{
  const f=await fixture();
  try {
    const old=f.selectServer('A'),current=f.selectServer('B');
    assert.equal(f.request.disabled,true,'pending readiness must disable requests');
    f.pending[1].resolve(ready('B'));await current;
    const accepted=f.snapshot();
    f.pending[0].resolve(ready('A',false));await old;
    assert.deepEqual(f.snapshot(),accepted);
    assert.equal(f.select.value,'B');
    assert.equal(f.request.disabled,false);
    assert.match(f.status.textContent,/B ready/);
  } finally {f.dispose();}
});

await test('older rejection cannot overwrite the latest successful result',async()=>{
  const f=await fixture();
  try {
    const old=f.selectServer('A'),current=f.selectServer('B');
    f.pending[1].resolve(ready('B'));await current;
    const accepted=f.snapshot();
    f.pending[0].reject(new Error('stale A error'));await old;
    assert.deepEqual(f.snapshot(),accepted);
    assert.equal(f.error.textContent,'');
  } finally {f.dispose();}
});

await test('current rejection stays visible and disabled after older success',async()=>{
  const f=await fixture();
  try {
    const old=f.selectServer('A'),current=f.selectServer('B');
    f.pending[1].reject(new Error('current B error'));await current;
    assert.equal(f.error.textContent,'current B error');
    assert.equal(f.request.disabled,true);
    const rejected=f.snapshot();
    f.pending[0].resolve(ready('A'));await old;
    assert.deepEqual(f.snapshot(),rejected);
  } finally {f.dispose();}
});

for(const settlement of ['resolve','reject']) {
  await test(`unmount ignores a pending ${settlement}`,async()=>{
    const f=await fixture();
    const pending=f.selectServer('B');
    const before=f.snapshot();
    f.dispose();
    if(settlement==='resolve')f.pending[0].resolve(ready('B'));
    else f.pending[0].reject(new Error('error after unmount'));
    await pending;
    assert.deepEqual(f.snapshot(),before);
  });
}

console.log(`Console readiness regression: ${scenarios} scenarios passed`);
