#include "config_store.h"
#include "frame.h"
#include "nvs.h"
#include "pb_encode.h"
#include <assert.h>
#include <stdio.h>
#include <string.h>
static uint8_t slots[2][528],staged[528];
static size_t lengths[2],staged_length;
static unsigned staged_index;
static bool fail_commit;
esp_err_t nvs_flash_init(void){return ESP_OK;}
esp_err_t nvs_open(const char *name,int mode,nvs_handle_t *h){(void)name;(void)mode;*h=1;return ESP_OK;}
esp_err_t nvs_get_blob(nvs_handle_t h,const char *name,void *data,size_t *n){(void)h;unsigned i=name[4]-'0';if(!lengths[i] || *n<lengths[i])return ESP_FAIL;*n=lengths[i];memcpy(data,slots[i],*n);return ESP_OK;}
esp_err_t nvs_set_blob(nvs_handle_t h,const char *name,const void *data,size_t n){(void)h;assert(n<=528);staged_index=name[4]-'0';memcpy(staged,data,n);staged_length=n;return ESP_OK;}
esp_err_t nvs_commit(nvs_handle_t h){(void)h;if(fail_commit)return ESP_FAIL;memcpy(slots[staged_index],staged,staged_length);lengths[staged_index]=staged_length;return ESP_OK;}
static void put32(uint8_t *p,uint32_t value){for(unsigned i=0;i<4;i++)p[i]=(uint8_t)(value>>(8*i));}
int main(void){
 assert(config_init()==ESP_OK);ek_Config c=ek_Config_init_zero,out;
 assert(!config_get(&out,true));assert(!out.has_password && !out.has_psk && out.tcp_port==24800 && out.ip==0xc0a801c8 && out.usb_vid==0x046d && out.usb_pid==0xc52b);
 strcpy(c.name,"test");strcpy(c.ssid,"wifi");c.usb_vid=c.usb_pid=1;c.ip=0xc0a801c8;c.gw=0xc0a80101;c.mask=0xffffff00;c.tcp_port=24800;
 ek_ConfigResult result=config_save(&c);assert(result.code==1 && result.field==3);
 c.has_password=true;strcpy(c.password,"password");c.has_psk=true;c.psk.size=16;memset(c.psk.bytes,0x42,16);
 result=config_save(&c);assert(result.code==0);assert(config_get(&out,true));assert(!out.has_password && !out.password[0] && !out.has_psk && !out.psk.size);
 strcpy(out.name,"renamed");out.six_kro=true;assert(config_save(&out).code==0);
 assert(config_get(&out,false));assert(out.has_password && !strcmp(out.password,"password") && out.has_psk && out.psk.bytes[0]==0x42 && out.six_kro);
 out.has_password=true;out.password[0]=0;assert(config_save(&out).code==0);
 out.has_psk=true;out.psk.size=0;result=config_save(&out);assert(result.code==1 && result.field==4);
 config_get(&out,true);strcpy(out.name,"not committed");fail_commit=true;assert(config_save(&out).code==2);fail_commit=false;
 assert(config_init()==ESP_OK);assert(config_get(&out,false));assert(!strcmp(out.name,"renamed") && out.password[0]==0 && out.six_kro);
 // Newer invalid CRC cannot create a mixed configuration or hide the valid slot.
 slots[1][lengths[1]-1]^=1;assert(config_init()==ESP_OK);assert(config_get(&out,false));assert(!strcmp(out.password,"password"));
 // v1 open-network blobs omitted the empty password; migrate its presence explicitly.
 c.has_password=false;c.password[0]=0;
 memcpy(slots[0],"EKCF",4);put32(slots[0]+4,1);put32(slots[0]+8,10);put32(slots[0]+12,1);
 pb_ostream_t encoded=pb_ostream_from_buffer(slots[0]+16,496);assert(pb_encode(&encoded,ek_Config_fields,&c));lengths[0]=20+encoded.bytes_written;
 put32(slots[0]+lengths[0]-4,frame_crc32(slots[0],lengths[0]-4));
 assert(config_init()==ESP_OK);assert(config_get(&out,false));assert(out.has_password && !out.password[0] && out.has_psk);
 assert(config_reset()==ESP_OK);assert(config_init()==ESP_OK);assert(!config_get(&out,true));
 puts("C optional credential merge, redaction, dual-slot and migration passed");return 0;
}
