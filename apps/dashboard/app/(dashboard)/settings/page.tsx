"use client";

import { Suspense } from "react";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { MessageCircle, Send } from "lucide-react";

function SettingsContent() {
  return (
    <div className="space-y-6 max-w-5xl">
      <div>
        <h1 className="text-2xl font-bold text-foreground">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Manage system configurations and notification integrations
        </p>
      </div>

      <div>
        <h2 className="text-lg font-semibold text-foreground mb-4">Notification Integrations</h2>
        
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <Card className="opacity-50 pointer-events-none">
            <CardHeader className="pb-4">
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-3">
                  <div className="p-2 rounded-md bg-muted text-muted-foreground">
                    <MessageCircle className="h-6 w-6" />
                  </div>
                  <div>
                    <CardTitle className="text-base text-muted-foreground">KakaoTalk</CardTitle>
                    <CardDescription className="text-xs mt-1">
                      Send trade alerts to yourself via KakaoTalk Memo API
                    </CardDescription>
                  </div>
                </div>
                <Badge variant="secondary">Disabled</Badge>
              </div>
            </CardHeader>
          </Card>

          <div className="space-y-6">
            <Card>
              <CardHeader className="pb-4">
                <div className="flex items-center gap-3">
                  <div className="p-2 rounded-md bg-[#5865F2]/10 text-[#5865F2]">
                    <MessageCircle className="h-6 w-6" />
                  </div>
                  <div>
                    <CardTitle className="text-base">Discord</CardTitle>
                    <CardDescription className="text-xs mt-1">
                      Configured via DISCORD_WEBHOOK_URL environment variable
                    </CardDescription>
                  </div>
                </div>
              </CardHeader>
            </Card>

            <Card>
              <CardHeader className="pb-4">
                <div className="flex items-center gap-3">
                  <div className="p-2 rounded-md bg-[#229ED9]/10 text-[#229ED9]">
                    <Send className="h-6 w-6" />
                  </div>
                  <div>
                    <CardTitle className="text-base">Telegram</CardTitle>
                    <CardDescription className="text-xs mt-1">
                      Configured via TELEGRAM_BOT_TOKEN environment variable
                    </CardDescription>
                  </div>
                </div>
              </CardHeader>
            </Card>
          </div>
        </div>
      </div>
    </div>
  );
}

export default function SettingsPage() {
  return (
    <Suspense fallback={<div>Loading settings...</div>}>
      <SettingsContent />
    </Suspense>
  );
}
