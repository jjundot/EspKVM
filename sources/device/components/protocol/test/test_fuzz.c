#include "frame.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
typedef struct {uint64_t digest;size_t count;} result;
static void receive(uint32_t seq,const uint8_t *body,size_t n,void *ctx){
    assert(seq && n && n<=512 && body);result *r=ctx;r->count++;
    r->digest=(r->digest*0x100000001b3ULL)^seq^frame_crc32(body,n);
}
int LLVMFuzzerTestOneInput(const uint8_t *data,size_t n){
    serial_parser bulk={0},fragments={0};result a={0},b={0};
    serial_parser_feed(&bulk,data,n,0,receive,&a);
    for(size_t i=0;i<n;){size_t size=1+data[i]%19;if(size>n-i)size=n-i;
        serial_parser_feed(&fragments,data+i,size,0,receive,&b);i+=size;
        assert(fragments.len<=sizeof(fragments.data));
    }
    for(unsigned i=1;i<=2;i++){
        serial_parser_feed(&bulk,NULL,0,i*1000,receive,&a);
        serial_parser_feed(&fragments,NULL,0,i*1000,receive,&b);
    }
    assert(a.count==b.count && a.digest==b.digest && bulk.len==fragments.len);
    assert(!memcmp(bulk.data,fragments.data,bulk.len));return 0;
}
#ifndef EK_LIBFUZZER
static uint32_t seed=0x45534b56;
static uint32_t random32(void){seed^=seed<<13;seed^=seed>>17;seed^=seed<<5;return seed;}
int main(void){
    uint8_t data[1024];
    for(unsigned test=0;test<10000;test++){
        size_t n=20+random32()%1005;for(size_t i=0;i<n;i++)data[i]=(uint8_t)random32();
        size_t start=random32()%(n-19);frame_header h={.len=4,.flags=2,.seq=random32()|1};
        assert(frame_header_encode(data+start,&h));memcpy(data+start+12,"EKVM",4);
        uint32_t crc=frame_crc32(data+start,16);for(unsigned i=0;i<4;i++)data[start+16+i]=(uint8_t)(crc>>(8*i));
        LLVMFuzzerTestOneInput(data,n);
    }
    puts("10000 randomized C parser fragmentation/recovery cases passed");return 0;
}
#endif
