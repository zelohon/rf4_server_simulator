const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');

test('fishing rule switch saves, reloads and fits supported sizes and DPI',async()=>{
 let enabled='0',distribution='0',failRead=false,failWrite=false,writes=0;
 const html=fs.readFileSync(path.join(__dirname,'web/index.html'));
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture'),key=url.pathname.replace(/^\/admin/,'');
  const json=(value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
  if(key==='/api/me')return json({username:'admin'});
  if(key==='/api/fish/areas')return json([]);
  if(key==='/api/tunables'){
   if(req.method==='GET')return failRead?json({error:'读取失败'},503):json({fish_bait_rules_enabled:enabled,fish_distribution_enabled:distribution});
   let body='';req.on('data',c=>body+=c);req.on('end',()=>{
    if(failWrite)return json({error:'保存失败'},503);
    const value=JSON.parse(body);assert.ok(['fish_bait_rules_enabled','fish_distribution_enabled'].includes(value.key));assert.ok(['0','1'].includes(value.value));
    if(value.key==='fish_bait_rules_enabled')enabled=value.value;else distribution=value.value;writes++;json({ok:true});
   });return;
  }
  if(key.startsWith('/api/'))return json({});
  res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);
 });
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 const metrics=[];
 try{
  for(const width of [1280,1024,360])for(const dpi of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:dpi});
   await context.addInitScript(()=>{localStorage.setItem('rf4_local_admin_token','fixture');localStorage.setItem('rf4_admin_token','fixture');});
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   enabled='0';distribution='0';await page.goto(`http://127.0.0.1:${server.address().port}/admin/#fish`,{waitUntil:'networkidle'});
   const toggle=page.locator('#fishRulesToggle');await toggle.waitFor();
   assert.equal(await toggle.textContent(),'开启');
   assert.equal(await page.locator('#fishRulesCard button').count(),2);
   const before=writes;await toggle.click();await page.waitForFunction(()=>document.getElementById('fishRulesToggle').getAttribute('aria-pressed')==='true');
   assert.equal(writes,before+1);assert.equal(distribution,'0');
   const dt=page.locator('#fishDistributionToggle');await dt.click();await page.waitForFunction(()=>document.getElementById('fishDistributionToggle').getAttribute('aria-pressed')==='true');assert.equal(enabled,'1');assert.equal(distribution,'1');
   await page.reload({waitUntil:'networkidle'});assert.equal(await toggle.textContent(),'关闭');assert.equal(await dt.textContent(),'关闭');
   await toggle.click();await page.waitForFunction(()=>document.getElementById('fishRulesToggle').getAttribute('aria-pressed')==='false');assert.equal(distribution,'1');
   await dt.click();await page.waitForFunction(()=>document.getElementById('fishDistributionToggle').getAttribute('aria-pressed')==='false');assert.equal(enabled,'0');
   await toggle.click();await page.waitForFunction(()=>document.getElementById('fishRulesToggle').getAttribute('aria-pressed')==='true');
   await page.locator('.toast').evaluate(e=>e.replaceChildren());
   for(const [name,text] of [['normal',null],['chinese','尚未识别的最长合理业务饵料名称与前置条件'.repeat(12)],['continuous','UnrecognizedBait'.repeat(30)]]){
    if(text)for(const id of ['fishRulesMessage','fishDistributionMessage'])await page.locator('#'+id).evaluate((e,text)=>e.textContent=text,text);
    const m=await page.locator('#fishRulesCard').evaluate(card=>{
     const rect=e=>{const r=e.getBoundingClientRect();return{x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height};};
     const rows=[...card.querySelectorAll('h2,small,p,button')].map(e=>{
      const range=document.createRange();range.selectNodeContents(e);
      return{tag:e.tagName,box:rect(e),font:getComputedStyle(e).fontSize,scroll:e.scrollWidth,client:e.clientWidth,textRects:[...range.getClientRects()].map(r=>({x:r.x,right:r.right,y:r.y,bottom:r.bottom}))};
     });
     return{viewport:innerWidth,document:document.documentElement.scrollWidth,card:rect(card),rows};
    });
    assert.ok(m.document<=width+2,JSON.stringify({width,dpi,name,m}));
    for(const r of m.rows){
     assert.ok(r.scroll<=r.client+2,JSON.stringify({width,dpi,name,r}));
     assert.ok(r.box.x>=m.card.x&&r.box.right<=m.card.right+1);
     for(const t of r.textRects)assert.ok(t.x>=r.box.x-1&&t.right<=r.box.right+1&&t.y>=r.box.y-1&&t.bottom<=r.box.bottom+1);
    }
    const title=m.rows.find(r=>r.tag==='H2').box,button=m.rows.find(r=>r.tag==='BUTTON').box;
    const pairs=await page.locator('#fishRulesCard .rule-row').evaluateAll(rows=>rows.map(row=>{const d=row.querySelector('div').getBoundingClientRect(),b=row.querySelector('button').getBoundingClientRect();return{right:d.right,bottom:d.bottom,x:b.x,y:b.y};}));
    for(const p of pairs)assert.ok(p.x>=p.right||p.y>=p.bottom-1,'description and button overlap');
    assert.ok(button.x>=title.right||button.y>=title.bottom-1,'title and button overlap');
    metrics.push({width,dpi,name,cardWidth:m.card.width,cardHeight:m.card.height,buttonWidth:button.width,buttonHeight:button.height,font:m.rows.find(r=>r.tag==='BUTTON').font});
    if(process.env.RF4_RULES_SCREENSHOT_DIR&&dpi===1.5&&name==='normal'&&width!==1024)await page.screenshot({path:path.join(process.env.RF4_RULES_SCREENSHOT_DIR,`rules-${width}.png`),fullPage:true});
   }
   failWrite=true;await toggle.click();await page.waitForFunction(()=>!document.getElementById('fishRulesToggle').disabled);assert.equal(enabled,'1');failWrite=false;
   await toggle.click();await page.waitForFunction(()=>document.getElementById('fishRulesToggle').getAttribute('aria-pressed')==='false');assert.equal(enabled,'0');
   failRead=true;await page.reload({waitUntil:'networkidle'});assert.equal(await toggle.isDisabled(),true);assert.equal(await dt.isDisabled(),true);assert.equal(await page.locator('#fishRulesBadge').textContent(),'读取失败');failRead=false;
   assert.deepEqual(errors,[]);await context.close();
  }
  console.log(JSON.stringify({renderedCases:metrics.length,measurements:metrics}));
 }finally{await browser.close();await new Promise(r=>server.close(r));}
});
