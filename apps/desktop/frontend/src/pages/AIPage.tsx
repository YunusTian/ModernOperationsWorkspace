import { FormEvent, useEffect, useRef, useState } from "react";
import { AIMessage, AIProviderVM, App, eventsOn } from "../bindings";

export default function AIPage() {
  const [providers,setProviders]=useState<AIProviderVM[]>([]);
  const [provider,setProvider]=useState(""); const [model,setModel]=useState("");
  const [messages,setMessages]=useState<AIMessage[]>([]); const [input,setInput]=useState("");
  const [busy,setBusy]=useState(false); const [error,setError]=useState(""); const [tool,setTool]=useState("");
  const session=useRef(""); const off=useRef<(()=>void)[]>([]);
  useEffect(()=>{App.AIProviders().then((p)=>{setProviders(p);if(p[0]){setProvider(p[0].name);setModel(p[0].capabilities.models?.[0]??"")}}).catch((e)=>setError(String(e)));return()=>{off.current.forEach((f)=>f());if(session.current)void App.AIChatClose(session.current)}},[]);
  const selected=providers.find((p)=>p.name===provider);
  async function send(e:FormEvent){e.preventDefault();const text=input.trim();if(!text||busy)return;setInput("");setError("");setTool("");const next=[...messages,{role:"user",content:text} as AIMessage];setMessages([...next,{role:"assistant",content:""}]);setBusy(true);
    try{const sid=await App.AIChatOpen({provider,model,messages:next,timeout_seconds:120});session.current=sid;off.current.forEach((f)=>f());off.current=[
      eventsOn(`ai:${sid}:delta`,(...d)=>{const chunk=String(d[0]??"");setMessages((old)=>old.map((m,i)=>i===old.length-1?{...m,content:(m.content??"")+chunk}:m))}),
      eventsOn(`ai:${sid}:tool`,(...d)=>setTool(JSON.stringify(d[0]??{},null,2))),
      eventsOn(`ai:${sid}:done`,(...d)=>{const payload=d[0] as {error?:string}|undefined;if(payload?.error)setError(payload.error);setBusy(false);session.current=""}),
    ];await App.AIChatStart(sid)}catch(e){setError(String(e));setBusy(false)}
  }
  function stop(){if(session.current)void App.AIChatClose(session.current);setBusy(false)}
  return <section className="ai-page"><header className="ai-toolbar"><h2>AI Workspace</h2><select value={provider} onChange={(e)=>{setProvider(e.target.value);const p=providers.find((x)=>x.name===e.target.value);setModel(p?.capabilities.models?.[0]??"")}}>{providers.map((p)=><option key={p.name}>{p.name}</option>)}</select><input value={model} onChange={(e)=>setModel(e.target.value)} placeholder="Model"/><span>{selected?.capabilities.tool_calls?"Tools available":"Chat only"}</span></header>
    <div className="ai-messages">{messages.length===0&&<div className="ai-empty">Ask about your infrastructure. AI remains behind the same Command Engine permissions.</div>}{messages.map((m,i)=><article className={`ai-msg ${m.role}`} key={i}><b>{m.role}</b><pre>{m.content}</pre></article>)}{tool&&<details open><summary>Tool call</summary><pre>{tool}</pre></details>}{error&&<div className="ai-error">{error}</div>}</div>
    <form className="ai-compose" onSubmit={send}><textarea value={input} onChange={(e)=>setInput(e.target.value)} placeholder="Ask MOW…" disabled={busy}/>{busy?<button type="button" onClick={stop}>Stop</button>:<button type="submit" disabled={!provider}>Send</button>}</form>
  </section>
}
