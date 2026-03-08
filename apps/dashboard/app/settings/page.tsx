"use client";

import { useEffect, useState, Suspense } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MessageCircle, Send } from "lucide-react";

interface KakaoStatus {
  connected: boolean;
  expires_at?: string;
  error?: string;
}

function SettingsContent() {
  const searchParams = useSearchParams();
  const router = useRouter();
  
  const [kakaoStatus, setKakaoStatus] = useState<KakaoStatus | null>(null);
  const [isLoadingStatus, setIsLoadingStatus] = useState(true);
  const [isConnecting, setIsConnecting] = useState(false);
  const [isDisconnecting, setIsDisconnecting] = useState(false);
  const [isTesting, setIsTesting] = useState(false);
  const [message, setMessage] = useState<{ type: "success" | "error"; text: string } | null>(null);

  useEffect(() => {
    const kakaoParam = searchParams.get("kakao");
    if (kakaoParam === "connected") {
      setMessage({ type: "success", text: "Successfully connected to KakaoTalk!" });
      router.replace("/settings");
    } else if (kakaoParam === "error") {
      setMessage({ type: "error", text: "Failed to connect to KakaoTalk. Please try again." });
      router.replace("/settings");
    }

    if (kakaoParam) {
      const timer = setTimeout(() => setMessage(null), 5000);
      return () => clearTimeout(timer);
    }
  }, [searchParams, router]);

  const fetchStatus = async () => {
    try {
      setIsLoadingStatus(true);
      const res = await fetch("/api/notifications/kakao/status");
      if (res.ok) {
        const data = await res.json();
        setKakaoStatus(data);
      } else {
        setKakaoStatus({ connected: false });
      }
    } catch (error) {
      console.error("Failed to fetch Kakao status:", error);
      setKakaoStatus({ connected: false });
    } finally {
      setIsLoadingStatus(false);
    }
  };

  useEffect(() => {
    fetchStatus();
  }, []);

  const handleConnect = async () => {
    try {
      setIsConnecting(true);
      const res = await fetch("/api/notifications/kakao/auth-url");
      const data = await res.json();
      if (data.url) {
        window.location.href = data.url;
      } else {
        throw new Error("No URL returned");
      }
    } catch (error) {
      console.error("Failed to get auth URL:", error);
      setMessage({ type: "error", text: "Failed to start connection process." });
      setIsConnecting(false);
    }
  };

  const handleDisconnect = async () => {
    try {
      setIsDisconnecting(true);
      const res = await fetch("/api/notifications/kakao/disconnect", {
        method: "DELETE",
      });
      if (res.ok) {
        setMessage({ type: "success", text: "Successfully disconnected KakaoTalk." });
        await fetchStatus();
      } else {
        throw new Error("Failed to disconnect");
      }
    } catch (error) {
      console.error("Failed to disconnect:", error);
      setMessage({ type: "error", text: "Failed to disconnect KakaoTalk." });
    } finally {
      setIsDisconnecting(false);
      setTimeout(() => setMessage(null), 5000);
    }
  };

  const handleTest = async () => {
    try {
      setIsTesting(true);
      const res = await fetch("/api/notifications/kakao/test", {
        method: "POST",
      });
      if (res.ok) {
        setMessage({ type: "success", text: "Test message sent successfully!" });
      } else {
        throw new Error("Failed to send test message");
      }
    } catch (error) {
      console.error("Failed to send test message:", error);
      setMessage({ type: "error", text: "Failed to send test message." });
    } finally {
      setIsTesting(false);
      setTimeout(() => setMessage(null), 5000);
    }
  };

  return (
    <div className="space-y-6 max-w-5xl">
      <div>
        <h1 className="text-2xl font-bold text-foreground">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Manage system configurations and notification integrations
        </p>
      </div>

      {message && (
        <div
          className={`p-3 rounded-md text-sm ${
            message.type === "success"
              ? "bg-emerald-500/10 border border-emerald-500/20 text-emerald-500"
              : "bg-red-500/10 border border-red-500/20 text-red-500"
          }`}
        >
          {message.text}
        </div>
      )}

      <div>
        <h2 className="text-lg font-semibold text-foreground mb-4">Notification Integrations</h2>
        
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader className="pb-4">
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-3">
                  <div className="p-2 rounded-md bg-[#FEE500]/10 text-[#FEE500]">
                    <MessageCircle className="h-6 w-6" />
                  </div>
                  <div>
                    <CardTitle className="text-base">KakaoTalk</CardTitle>
                    <CardDescription className="text-xs mt-1">
                      Send trade alerts to yourself via KakaoTalk Memo API
                    </CardDescription>
                  </div>
                </div>
                {!isLoadingStatus && kakaoStatus && (
                  <Badge
                    variant={kakaoStatus.connected ? "outline" : "secondary"}
                    className={
                      kakaoStatus.connected
                        ? "text-emerald-500 border-emerald-500/20 bg-emerald-500/10"
                        : ""
                    }
                  >
                    {kakaoStatus.connected ? "Connected" : "Not Set"}
                  </Badge>
                )}
              </div>
            </CardHeader>
            <CardContent>
              {isLoadingStatus ? (
                <div className="h-10 flex items-center text-sm text-muted-foreground">
                  Checking status...
                </div>
              ) : kakaoStatus?.connected ? (
                <div className="space-y-4">
                  {kakaoStatus.expires_at && (
                    <div className="text-xs text-muted-foreground bg-accent/50 p-2 rounded-md border border-border/50">
                      Token expires at: {new Date(kakaoStatus.expires_at).toLocaleString()}
                    </div>
                  )}
                  <div className="flex items-center gap-3">
                    <Button
                      variant="outline"
                      onClick={handleTest}
                      disabled={isTesting}
                      className="flex-1 gap-2"
                    >
                      <Send className="h-4 w-4" />
                      {isTesting ? "Sending..." : "Send Test"}
                    </Button>
                    <Button
                      variant="destructive"
                      onClick={handleDisconnect}
                      disabled={isDisconnecting}
                      className="flex-1"
                    >
                      {isDisconnecting ? "Disconnecting..." : "Disconnect"}
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="space-y-4">
                  <Button
                    onClick={handleConnect}
                    disabled={isConnecting}
                    className="w-full bg-[#FEE500] hover:bg-[#FEE500]/90 text-black font-semibold"
                  >
                    {isConnecting ? "Connecting..." : "Connect KakaoTalk"}
                  </Button>
                </div>
              )}
            </CardContent>
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
