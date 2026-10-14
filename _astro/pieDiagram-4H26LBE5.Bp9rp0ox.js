import{F as S,be as R,g as K,a1 as Q,b1 as Y,a2 as tt,b2 as et,a6 as at,b4 as nt,b as d,aE as F,a4 as rt,s as it,b0 as st,aN as ot,E as lt,t as ct,S as ut}from"./mermaid.core.riUnZhlN.js";import{p as dt}from"./chunk-4BX2VUAB.PAjTN22n.js";import{p as pt}from"./wardley-L42UT6IY.jwIjNpBb.js";import{d as I}from"./arc.BGplRJPE.js";import{o as gt}from"./ordinal.BYWQX77i.js";function ft(t,a){return a<t?-1:a>t?1:a>=t?0:NaN}function ht(t){return t}function mt(){var t=ht,a=ft,f=null,y=S(0),s=S(R),p=S(0);function o(e){var r,l=(e=K(e)).length,g,h,v=0,c=new Array(l),i=new Array(l),x=+y.apply(this,arguments),w=Math.min(R,Math.max(-R,s.apply(this,arguments)-x)),m,D=Math.min(Math.abs(w)/l,p.apply(this,arguments)),$=D*(w<0?-1:1),u;for(r=0;r<l;++r)(u=i[c[r]=r]=+t(e[r],r,e))>0&&(v+=u);for(a!=null?c.sort(function(A,C){return a(i[A],i[C])}):f!=null&&c.sort(function(A,C){return f(e[A],e[C])}),r=0,h=v?(w-l*$)/v:0;r<l;++r,x=m)g=c[r],u=i[g],m=x+(u>0?u*h:0)+$,i[g]={data:e[g],index:r,value:u,startAngle:x,endAngle:m,padAngle:D};return i}return o.value=function(e){return arguments.length?(t=typeof e=="function"?e:S(+e),o):t},o.sortValues=function(e){return arguments.length?(a=e,f=null,o):a},o.sort=function(e){return arguments.length?(f=e,a=null,o):f},o.startAngle=function(e){return arguments.length?(y=typeof e=="function"?e:S(+e),o):y},o.endAngle=function(e){return arguments.length?(s=typeof e=="function"?e:S(+e),o):s},o.padAngle=function(e){return arguments.length?(p=typeof e=="function"?e:S(+e),o):p},o}var vt=ut.pie,W={sections:new Map,showData:!1},T=W.sections,z=W.showData,xt=structuredClone(vt),St=d(()=>structuredClone(xt),"getConfig"),yt=d(()=>{T=new Map,z=W.showData,ct()},"clear"),wt=d(({label:t,value:a})=>{if(a<0)throw new Error(`"${t}" has invalid value: ${a}. Negative values are not allowed in pie charts. All slice values must be >= 0.`);T.has(t)||(T.set(t,a),F.debug(`added new section: ${t}, with value: ${a}`))},"addSection"),At=d(()=>T,"getSections"),Ct=d(t=>{z=t},"setShowData"),Dt=d(()=>z,"getShowData"),_={getConfig:St,clear:yt,setDiagramTitle:nt,getDiagramTitle:at,setAccTitle:et,getAccTitle:tt,setAccDescription:Y,getAccDescription:Q,addSection:wt,getSections:At,setShowData:Ct,getShowData:Dt},$t=d((t,a)=>{dt(t,a),a.setShowData(t.showData),t.sections.map(a.addSection)},"populateDb"),Tt={parse:d(async t=>{const a=await pt("pie",t);F.debug(a),$t(a,_)},"parse")},bt=d(t=>`
  .pieCircle{
    stroke: ${t.pieStrokeColor};
    stroke-width : ${t.pieStrokeWidth};
    opacity : ${t.pieOpacity};
  }
  .pieOuterCircle{
    stroke: ${t.pieOuterStrokeColor};
    stroke-width: ${t.pieOuterStrokeWidth};
    fill: none;
  }
  .pieTitleText {
    text-anchor: middle;
    font-size: ${t.pieTitleTextSize};
    fill: ${t.pieTitleTextColor};
    font-family: ${t.fontFamily};
  }
  .slice {
    font-family: ${t.fontFamily};
    fill: ${t.pieSectionTextColor};
    font-size:${t.pieSectionTextSize};
    // fill: white;
  }
  .legend text {
    fill: ${t.pieLegendTextColor};
    font-family: ${t.fontFamily};
    font-size: ${t.pieLegendTextSize};
  }
`,"getStyles"),Et=bt,kt=d(t=>{const a=[...t.values()].reduce((s,p)=>s+p,0),f=[...t.entries()].map(([s,p])=>({label:s,value:p})).filter(s=>s.value/a*100>=1);return mt().value(s=>s.value).sort(null)(f)},"createPieArcs"),Mt=d((t,a,f,y)=>{F.debug(`rendering pie chart
`+t);const s=y.db,p=rt(),o=it(s.getConfig(),p.pie),e=40,r=18,l=4,g=450,h=g,v=st(a),c=v.append("g");c.attr("transform","translate("+h/2+","+g/2+")");const{themeVariables:i}=p;let[x]=ot(i.pieOuterStrokeWidth);x??=2;const w=o.textPosition,m=Math.min(h,g)/2-e,D=I().innerRadius(0).outerRadius(m),$=I().innerRadius(m*w).outerRadius(m*w);c.append("circle").attr("cx",0).attr("cy",0).attr("r",m+x/2).attr("class","pieOuterCircle");const u=s.getSections(),A=kt(u),C=[i.pie1,i.pie2,i.pie3,i.pie4,i.pie5,i.pie6,i.pie7,i.pie8,i.pie9,i.pie10,i.pie11,i.pie12];let b=0;u.forEach(n=>{b+=n});const N=A.filter(n=>(n.data.value/b*100).toFixed(0)!=="0"),E=gt(C).domain([...u.keys()]);c.selectAll("mySlices").data(N).enter().append("path").attr("d",D).attr("fill",n=>E(n.data.label)).attr("class","pieCircle"),c.selectAll("mySlices").data(N).enter().append("text").text(n=>(n.data.value/b*100).toFixed(0)+"%").attr("transform",n=>"translate("+$.centroid(n)+")").style("text-anchor","middle").attr("class","slice");const V=c.append("text").text(s.getDiagramTitle()).attr("x",0).attr("y",-400/2).attr("class","pieTitleText"),G=[...u.entries()].map(([n,M])=>({label:n,value:M})),k=c.selectAll(".legend").data(G).enter().append("g").attr("class","legend").attr("transform",(n,M)=>{const P=r+l,q=P*G.length/2,H=12*r,J=M*P-q;return"translate("+H+","+J+")"});k.append("rect").attr("width",r).attr("height",r).style("fill",n=>E(n.label)).style("stroke",n=>E(n.label)),k.append("text").attr("x",r+l).attr("y",r-l).text(n=>s.getShowData()?`${n.label} [${n.value}]`:n.label);const U=Math.max(...k.selectAll("text").nodes().map(n=>n?.getBoundingClientRect().width??0)),j=h+e+r+l+U,L=V.node()?.getBoundingClientRect().width??0,X=h/2-L/2,Z=h/2+L/2,B=Math.min(0,X),O=Math.max(j,Z)-B;v.attr("viewBox",`${B} 0 ${O} ${g}`),lt(v,g,O,o.useMaxWidth)},"draw"),Rt={draw:Mt},Bt={parser:Tt,db:_,renderer:Rt,styles:Et};export{Bt as diagram};
