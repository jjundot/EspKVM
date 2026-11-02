// Independent oracle: Node crypto and explicit protobuf wire encoding, no Go/C runtime.
const fs = require('node:fs');
const crypto = require('node:crypto');
function varint(n) { n=BigInt(n);const a=[];do {let b=Number(n&127n);n>>=7n;a.push(b|(n?128:0));}while(n);return Buffer.from(a); }
const cat=(...a)=>Buffer.concat(a);
const integer=(tag,n)=>cat(varint(tag*8),varint(n));
const bytes=(tag,b)=>cat(varint(tag*8+2),varint(b.length),b);
const text=(tag,s)=>bytes(tag,Buffer.from(s));
const signed=(tag,n)=>integer(tag,n<0?-n*2-1:n*2);
const header=(length,flags=0,seq=0)=>{const b=Buffer.alloc(12);b.write('EKVM');b.writeUInt16LE(length,4);b[6]=flags;b.writeUInt32LE(seq,8);return b;};
const rows={};const add=(name,b)=>(rows[name]=b,b);
const psk=add('psk',Buffer.from('000102030405060708090a0b0c0d0e0f','hex'));
const nonce=add('nonce',Buffer.from('101112131415161718191a1b1c1d1e1f','hex'));
const owner=add('owner',Buffer.from('202122232425262728292a2b2c2d2e2f','hex'));
const ch=bytes(1,cat(integer(1,2),bytes(2,Buffer.alloc(16,0x42)),integer(3,0x10000),bytes(4,Buffer.from('020000000001','hex')),integer(5,1),text(6,'fixture')));
const challenge=add('challenge_frame',cat(header(ch.length),ch));
const transcript=add('transcript',crypto.createHash('sha256').update(challenge).digest());
const context=add('context',crypto.createHash('sha256').update(cat(Buffer.from('EK-AUTH-V2'),transcript,owner,Buffer.from([1]))).digest());
const proof=label=>crypto.createHmac('sha256',psk).update(cat(Buffer.from(label),context,nonce)).digest();
const pc=add('proof_c',proof('EK-AUTH-C'));add('proof_d',proof('EK-AUTH-D'));
const key=add('key',proof('EK-KEY').subarray(0,16));
add('pb_keyboard_zero',bytes(16,Buffer.alloc(0)));
const release=add('pb_release',bytes(19,integer(1,1)));
const mouse=add('pb_mouse',bytes(17,cat(integer(1,31),signed(2,-1000000),signed(3,1000000),signed(4,-127),signed(5,127))));
add('pb_auth',bytes(2,cat(bytes(1,nonce),bytes(2,pc),bytes(3,owner),integer(4,1))));
add('pb_heartbeat',bytes(32,cat(integer(1,0xffffffff),integer(2,1))));
add('pb_password_empty',bytes(50,bytes(3,Buffer.alloc(0))));
add('pb_config',bytes(50,cat(text(1,'N'.repeat(31)),text(2,'S'.repeat(32)),text(3,'a'.repeat(64)),bytes(4,psk),integer(5,65535),integer(6,65535),integer(7,0xc0a801c8),integer(8,0xc0a80101),integer(9,0xffffff00),integer(10,65535),integer(11,1))));
function seal(name,body,dir) {const h=header(body.length,1);const iv=Buffer.alloc(12);iv[0]=dir;const c=crypto.createCipheriv('aes-128-gcm',key,iv);c.setAAD(h);add(name,cat(h,c.update(body),c.final(),c.getAuthTag()));}
seal('release_frame',release,0);seal('mouse_frame',mouse,1);
fs.writeFileSync('sources/protocol/testdata/v2.txt',Object.entries(rows).map(([k,v])=>k+' '+v.toString('hex')).join('\n')+'\n');

function crc32(data) {let crc=0xffffffff;for(const v of data){crc^=v;for(let i=0;i<8;i++)crc=(crc>>>1)^((crc&1)?0xedb88320:0);}return (crc^0xffffffff)>>>0;}
function serial(body,seq){let raw=cat(header(body.length,2,seq),body);let crc=Buffer.alloc(4);crc.writeUInt32LE(crc32(raw));return cat(raw,crc);}
const frames={info_get:serial(cat(integer(1,2),bytes(16,Buffer.alloc(0))),1),embedded_magic:serial(Buffer.from('EKVM'),42),maximum:serial(Buffer.alloc(512,0x78),0xffffffff)};
fs.writeFileSync('sources/protocol/testdata/frames.txt',Object.entries(frames).map(([k,v])=>k+' '+v.toString('hex')).join('\n')+'\n');
