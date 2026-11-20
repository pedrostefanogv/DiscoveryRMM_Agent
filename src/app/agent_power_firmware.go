package app

// ── Capacidade de reinício direto no firmware (BIOS/UEFI) ──
//
// O comando `shutdown /r /fw` reinicia a máquina direto no setup do firmware.
// Ele só existe em máquinas com boot UEFI: em BIOS legado (Legacy/CSM) o Windows
// recusa a operação. O verbo "restart-firmware" valida isso antes de agir e
// devolve uma falha tipada em vez de agendar um reinício que não faz nada.
//
// firmwareBootSupportFunc é o seam de teste: em produção aponta para
// probeFirmwareBootSupport (implementação por plataforma). Testes podem
// substituí-lo para simular máquinas UEFI/Legacy.
var firmwareBootSupportFunc = probeFirmwareBootSupport
