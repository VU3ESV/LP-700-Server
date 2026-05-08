Attribute VB_Name = "Module2"
Option Explicit

' Set these to match the values in the device's firmware and INF file.
Public Const MyVendorID = &H4D8 ' = LP - 500 ' 4660=ME note that this number is in hex, not decimal
Public Const MyProductID = &H1      '&HF869 '= LP - 500 ' 1=ME note that this number is in hex, not decimal
Public MyDeviceDetected As Boolean
 
Public Capabilities As HIDP_CAPS
Public DeviceAttributes As HIDD_ATTRIBUTES

Dim HID_Write_Handle As Long
Dim HID_Read_Handle As Long

Dim DataString As String
Dim DetailData As Long
Dim DetailDataBuffer() As Byte
Dim DevicePathName As String
Dim DeviceInfoSet As Long
Dim ErrorString As String

Dim EventObject As Long
Dim HIDOverlapped As OVERLAPPED

Dim LastDevice As Boolean
Dim MyDeviceInfoData As SP_DEVINFO_DATA
Dim MyDeviceInterfaceDetailData As SP_DEVICE_INTERFACE_DETAIL_DATA
Dim MyDeviceInterfaceData As SP_DEVICE_INTERFACE_DATA
Dim Needed As Long

Dim PreparsedData As Long
Dim Security As SECURITY_ATTRIBUTES

Public Function FindTheHid() As Boolean

    Dim Count As Integer
    Dim GUIDString As String
    Dim HidGuid As GUID
    Dim MemberIndex As Long
    Dim result As Long
    
    FindTheHid = False
    
    LastDevice = False
    MyDeviceDetected = False
    
    Security.lpSecurityDescriptor = 0
    Security.bInheritHandle = True
    Security.nLength = Len(Security)
    
    result = HidD_GetHidGuid(HidGuid)
    
    GUIDString = Hex$(HidGuid.Data1) & "-" & Hex$(HidGuid.Data2) & "-" & Hex$(HidGuid.Data3) & "-"
    
    For Count = 0 To 7
    
        If HidGuid.Data4(Count) >= &H10 Then
            GUIDString = GUIDString & Hex$(HidGuid.Data4(Count)) & " "
        Else
            GUIDString = GUIDString & "0" & Hex$(HidGuid.Data4(Count)) & " "
        End If
        
    Next Count
    
    DeviceInfoSet = SetupDiGetClassDevs(HidGuid, vbNullString, 0, (DIGCF_PRESENT Or DIGCF_DEVICEINTERFACE))
        
    DataString = GetDataString(DeviceInfoSet, 32)
    
    MemberIndex = 0
    
    Do
        
        MyDeviceInterfaceData.cbSize = LenB(MyDeviceInterfaceData)
        result = SetupDiEnumDeviceInterfaces(DeviceInfoSet, 0, HidGuid, MemberIndex, MyDeviceInterfaceData)
        
        If result = 0 Then LastDevice = True
        
        If result <> 0 Then
        
            MyDeviceInfoData.cbSize = Len(MyDeviceInfoData)
            result = SetupDiGetDeviceInterfaceDetail(DeviceInfoSet, MyDeviceInterfaceData, 0, 0, Needed, 0)
            
            DetailData = Needed
            ReDim DetailDataBuffer(Needed)
                
            MyDeviceInterfaceDetailData.cbSize = Len(MyDeviceInterfaceDetailData)
                        
            Call RtlMoveMemory(DetailDataBuffer(0), MyDeviceInterfaceDetailData, 4)
            
            result = SetupDiGetDeviceInterfaceDetail(DeviceInfoSet, MyDeviceInterfaceData, VarPtr(DetailDataBuffer(0)), DetailData, Needed, 0)
            
            DevicePathName = CStr(DetailDataBuffer())
            DevicePathName = StrConv(DevicePathName, vbUnicode)
            DevicePathName = Right$(DevicePathName, Len(DevicePathName) - 4)
                    
            HID_Write_Handle = CreateFile(DevicePathName, GENERIC_READ Or GENERIC_WRITE, (FILE_SHARE_READ Or FILE_SHARE_WRITE), Security, OPEN_EXISTING, 0&, 0)
                
            DeviceAttributes.Size = LenB(DeviceAttributes)
            result = HidD_GetAttributes(HID_Write_Handle, DeviceAttributes)
                
            'Find out if the device matches the one we're looking for.
            If (DeviceAttributes.VendorID = MyVendorID) And (DeviceAttributes.ProductID = MyProductID) Then
                    MyDeviceDetected = True
            Else
                    MyDeviceDetected = False
                    result = CloseHandle(HID_Write_Handle)
            End If
    
        End If
        
        MemberIndex = MemberIndex + 1
        
    Loop Until (LastDevice = True) Or (MyDeviceDetected = True)
    
    result = SetupDiDestroyDeviceInfoList(DeviceInfoSet)
    
    If MyDeviceDetected = True Then
    
        FindTheHid = True
        Call GetDeviceCapabilities
        HID_Read_Handle = CreateFile(DevicePathName, (GENERIC_READ Or GENERIC_WRITE), (FILE_SHARE_READ Or FILE_SHARE_WRITE), Security, OPEN_EXISTING, FILE_FLAG_OVERLAPPED, 0)
        Call PrepareForOverlappedTransfer
    
    End If
    
End Function
    
Public Function Close_the_HID()

    Dim Result1 As Long, Result2 As Long
    
    Result1 = CloseHandle(HID_Write_Handle)
    Result2 = CloseHandle(HID_Read_Handle)

    MyDeviceDetected = False
    
    Close_the_HID = Result1 + Result2
    
End Function
    
' Send data to the device.
' Pass in a buffer with the data to send, and the number of bytes in the buffer
' Function returns number of characters actually written
' Data array index starts at 1, but element 0 must exist (holds the report ID)

Public Function WriteToHID(ByRef DataToSend() As Byte, Optional Datalength As Long = 64) As Long
    
    Dim NumberOfBytesWritten As Long
    Dim ByteValue As String
    Dim result As Long
    
    'The first byte is the Report ID
    DataToSend(0) = 0
    
    NumberOfBytesWritten = 0
    
    result = WriteFile(HID_Write_Handle, DataToSend(0), Datalength + 1, NumberOfBytesWritten, 0)

    WriteToHID = NumberOfBytesWritten - 1
    
End Function

' Read data from the device and puts it into ReceiveBuffer()
' array index starts at 1, but array must include element 0 (holds the report ID)
' Function returns an error code:
' WAIT_OBJECT_0 (0) = read was successful
' WAIT_TIMEOUT (258) = no response within timeout limit
' other code = some other, undefined error

Public Function ReadFromHID(ByRef ReceiveBuffer() As Byte, Optional Datalength As Long = 64, Optional Timeout_in_msec As Integer = 5000) As Long
  
    Dim NumberOfBytesRead As Long
    Dim result As Long
    
    result = ReadFile(HID_Read_Handle, ReceiveBuffer(0), Datalength + 1, NumberOfBytesRead, HIDOverlapped)
    result = WaitForSingleObject(EventObject, Timeout_in_msec)
    Call ResetEvent(EventObject)

    If result <> WAIT_OBJECT_0 Then ' if there was a timeout or some other error
        
        result = CancelIo(HID_Read_Handle)
        CloseHandle (HID_Write_Handle)
        CloseHandle (HID_Read_Handle)
        MyDeviceDetected = False

    End If

    ReadFromHID = result
    
End Function

Private Function GetDataString(Address As Long, Bytes As Long) As String
    
    Dim Offset As Integer
    Dim result$
    Dim ThisByte As Byte
    
    For Offset = 0 To Bytes - 1
        
        Call RtlMoveMemory(ByVal VarPtr(ThisByte), ByVal Address + Offset, 1)
        
        If (ThisByte And &HF0) = 0 Then
            result$ = result$ & "0"
        End If
        
        result$ = result$ & Hex$(ThisByte) & " "
    
    Next Offset
    
    GetDataString = result$
    
End Function

Private Sub GetDeviceCapabilities()

    Dim ppData(29) As Byte
    Dim ppDataString As Variant
    Dim ValueCaps(1023) As Byte 'This is a guess. The byte array holds the structures.
    Dim result As Long
    
    result = HidD_GetPreparsedData(HID_Write_Handle, PreparsedData)
    
    result = RtlMoveMemory(ppData(0), PreparsedData, 30)
    
    ppDataString = ppData()
    
    ppDataString = StrConv(ppDataString, vbUnicode)
    
    result = HidP_GetCaps(PreparsedData, Capabilities)
    result = HidP_GetValueCaps(HidP_Input, ValueCaps(0), Capabilities.NumberInputValueCaps, PreparsedData)
       
    result = HidD_FreePreparsedData(PreparsedData)

End Sub

Private Sub PrepareForOverlappedTransfer()

    If EventObject = 0 Then EventObject = CreateEvent(Security, True, True, "")
        
    HIDOverlapped.Offset = 0
    HIDOverlapped.OffsetHigh = 0
    HIDOverlapped.hEvent = EventObject

End Sub



