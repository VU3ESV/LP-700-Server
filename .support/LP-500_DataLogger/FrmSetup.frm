VERSION 5.00
Begin VB.Form FrmMain 
   BackColor       =   &H80000004&
   Caption         =   "LP-500 DataLogger"
   ClientHeight    =   7845
   ClientLeft      =   120
   ClientTop       =   450
   ClientWidth     =   3585
   DrawWidth       =   2
   FillColor       =   &H00FFFFFF&
   FillStyle       =   0  'Solid
   ForeColor       =   &H000000FF&
   Icon            =   "FrmSetup.frx":0000
   LinkTopic       =   "Form1"
   ScaleHeight     =   7845
   ScaleWidth      =   3585
   StartUpPosition =   3  'Windows Default
   Begin VB.CommandButton cmdFreeze 
      Caption         =   "Freeze"
      Height          =   375
      Left            =   2040
      TabIndex        =   9
      Top             =   5160
      Width           =   975
   End
   Begin VB.CommandButton btn8 
      Caption         =   "Button 8"
      Height          =   375
      Left            =   2040
      TabIndex        =   8
      Top             =   4560
      Width           =   975
   End
   Begin VB.CommandButton btn7 
      Caption         =   "Button 7"
      Height          =   375
      Left            =   2040
      TabIndex        =   7
      Top             =   3960
      Width           =   975
   End
   Begin VB.CommandButton btn6 
      Caption         =   "Button 6"
      Height          =   375
      Left            =   2040
      TabIndex        =   6
      Top             =   3360
      Width           =   975
   End
   Begin VB.CommandButton btn5 
      Caption         =   "Button 5"
      Height          =   375
      Left            =   2040
      TabIndex        =   5
      Top             =   2760
      Width           =   975
   End
   Begin VB.CommandButton btn4 
      Caption         =   "Button 4"
      Height          =   375
      Left            =   2040
      TabIndex        =   4
      Top             =   2160
      Width           =   975
   End
   Begin VB.CommandButton btn3 
      Caption         =   "Button 3"
      Height          =   375
      Left            =   2040
      TabIndex        =   3
      Top             =   1560
      Width           =   975
   End
   Begin VB.CommandButton btn2 
      Caption         =   "Button 2"
      Height          =   375
      Left            =   2040
      TabIndex        =   2
      Top             =   960
      Width           =   975
   End
   Begin VB.CommandButton btn1 
      Caption         =   "Button 1"
      Height          =   375
      Left            =   2040
      TabIndex        =   1
      Top             =   360
      Width           =   975
   End
   Begin VB.ListBox lstResults 
      Height          =   7665
      Left            =   240
      TabIndex        =   0
      Top             =   120
      Width           =   1575
   End
   Begin VB.Timer tmrDelay 
      Left            =   2520
      Top             =   6240
   End
   Begin VB.Timer tmrContinuousDataCollect 
      Left            =   2280
      Top             =   6960
   End
End
Attribute VB_Name = "FrmMain"
Attribute VB_GlobalNameSpace = False
Attribute VB_Creatable = False
Attribute VB_PredeclaredId = True
Attribute VB_Exposed = False
Option Explicit
Dim Mode As Byte
Dim OutputReportData(65) As Byte
Dim InputReportData(65) As Byte

Private Declare Sub Sleep Lib "kernel32" (ByVal dwMilliseconds As Long)

Private Sub btn1_Click()   'Mode button

tmrContinuousDataCollect.Enabled = False
  If MyDeviceDetected = True Then
    OutputReportData(1) = 55
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn2_Click()    'Channel button

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 56
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn3_Click()     'Range button

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 57
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn4_Click()

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 58
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn5_Click()

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 59
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)

tmrContinuousDataCollect.Enabled = True
End Sub

Private Sub btn6_Click()

tmrContinuousDataCollect.Enabled = False
  If Mode <> 0 Then
    If MyDeviceDetected = True Then
      OutputReportData(1) = 60
      Call WriteToHID(OutputReportData)
    End If
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn7_Click()

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 61
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub btn8_Click()

tmrContinuousDataCollect.Enabled = False

  If MyDeviceDetected = True Then
    OutputReportData(1) = 62
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)
tmrContinuousDataCollect.Enabled = True

End Sub

Private Sub cmdFreeze_Click()

  If MyDeviceDetected = True Then
    OutputReportData(1) = 63
    Call WriteToHID(OutputReportData)
  End If
  Sleep (500)

End Sub

Private Sub Form_Load()

    FrmMain.Show
    FrmMain.Hide
    tmrDelay.Enabled = False
    Call Startup
    FrmMain.Show

End Sub

Private Sub Form_Unload(Cancel As Integer)

    Call Shutdown

End Sub

Private Sub Shutdown()

    Dim result As Long
    
    result = Close_the_HID
    
End Sub

Private Sub Startup()

    tmrContinuousDataCollect.Enabled = False
    tmrContinuousDataCollect.Interval = 100   'in ms
    
    MyDeviceDetected = FindTheHid
    
    If Not MyDeviceDetected Then

        Call MsgBox("Cannot find HID device LP-500", vbOKOnly, "No device detected!")
        Call Shutdown
        End
    Else
        lstResults.Clear
        lstResults.AddItem "USB HID device connected"
        lstResults.AddItem "VID = 0x" & Hex(MyVendorID)
        lstResults.AddItem "PID = 0x" & Hex(MyProductID)
        lstResults.AddItem ""
    End If
'    cnt = 0
    tmrContinuousDataCollect.Enabled = True
    
End Sub

Private Sub tmrContinuousDataCollect_Timer()

    Call ReadAndWriteToDevice

End Sub

Private Sub ReadAndWriteToDevice()

    Dim i, x, J As Long
    Dim base As Long
    Dim level As Byte
    Dim Power, SWR As String
    
    lstResults.Clear
    
'    lstResults.AddItem "***** HID Test Report *****"
    
    OutputReportData(1) = 48
    If MyDeviceDetected = True Then
      Call WriteToHID(OutputReportData)
      Call ReadFromHID(InputReportData)
    End If
  
    lstResults.AddItem "Pk Pwr Hi byte =" & InputReportData(1)
    lstResults.AddItem "Pk Pwr Lo byte =" & InputReportData(2)
    lstResults.AddItem "SWR =" & InputReportData(3)
    lstResults.AddItem "Mode =" & InputReportData(4)
    lstResults.AddItem "CH =" & InputReportData(5)
    lstResults.AddItem "Auto Ch =" & InputReportData(6)
    lstResults.AddItem "Rng =" & InputReportData(7)
    lstResults.AddItem "Alrm =" & InputReportData(8)
    lstResults.AddItem "Pk/Av =" & InputReportData(9)
    lstResults.AddItem "Alrm Set =" & InputReportData(10)
    lstResults.AddItem "Wfm CH =" & InputReportData(11)
    lstResults.AddItem "Wfm Rng" & InputReportData(12)
    lstResults.AddItem "Wfm Pst =" & InputReportData(13)
    lstResults.AddItem "Wfm Style=" & InputReportData(14)
    lstResults.AddItem "TT =" & InputReportData(15)
    lstResults.AddItem "Trig =" & InputReportData(16)
    lstResults.AddItem "Sweep =" & InputReportData(17)
    lstResults.AddItem "Knob =" & InputReportData(18)
    lstResults.AddItem "FFT CH =" & InputReportData(19)
    lstResults.AddItem "FFT Gain =" & InputReportData(20)
    lstResults.AddItem "FFT BW =" & InputReportData(21)
    lstResults.AddItem "FFT Avg =" & InputReportData(22)
    lstResults.AddItem "WFM AutoGain =" & InputReportData(23)
    lstResults.AddItem "Pk Pwr Hi byte =" & InputReportData(24)
    lstResults.AddItem "Pk Pwr Lo byte =" & InputReportData(25)
    lstResults.AddItem "Avg Pwr Hi byte =" & InputReportData(26)
    lstResults.AddItem "Avg Pwr Lo byte =" & InputReportData(27)
    lstResults.AddItem "BG_avg =" & InputReportData(28)
    lstResults.AddItem "BG_pk =" & InputReportData(29)
    lstResults.AddItem "BG_SWR =" & InputReportData(30)
    lstResults.AddItem "Pwr Mult Hi =" & InputReportData(31)
    lstResults.AddItem "Pwr Mult Lo =" & InputReportData(32)
    lstResults.AddItem "Pwr Mult =" & Int((InputReportData(32) + InputReportData(31) * 256) / 2)
    lstResults.AddItem "Peak / Avg =" & InputReportData(33)
    lstResults.AddItem "Filter =" & InputReportData(34)
    lstResults.AddItem "Freeze =" & InputReportData(35)
    Mode = InputReportData(4)
End Sub


